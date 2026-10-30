// Package poller walks the open watchlist, queries ADS-B, latches flight
// entries onto an aircraft once seen, and records the track.
package poller

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
	"github.com/avanha/fullflight/internal/aerodatabox"
	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/airlines"
	"github.com/avanha/fullflight/internal/airports"
	"github.com/avanha/fullflight/internal/geo"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

// Source is the subset of adsb.Client the poller needs.
type Source interface {
	Hex(ctx context.Context, hex string) ([]adsb.Aircraft, error)
	Callsign(ctx context.Context, cs string) ([]adsb.Aircraft, error)
	Reg(ctx context.Context, reg string) ([]adsb.Aircraft, error)
}

// AirframeLookup resolves a registration to a hex address.
type AirframeLookup interface {
	ByTail(ctx context.Context, tail string) (*airframes.Airframe, error)
}

// FlightLookup finds a flight by IATA number and departure date (AeroDataBox). It is the fallback for a
// flight that ADS-B cannot find or has lost, such as one over an ocean.
type FlightLookup interface {
	ByNumber(ctx context.Context, number, date string) ([]aerodatabox.Flight, error)
}

// TextSearcher finds recent messages containing a string (airframes.io), across all aircraft.
type TextSearcher interface {
	SearchText(ctx context.Context, s string) ([]airframes.Message, error)
}

type Poller struct {
	Store     *store.Store
	ADSB      Source
	Airframes AirframeLookup // optional
	Flights   FlightLookup   // optional
	Search    TextSearcher   // optional: last resort for a flight nothing else can find
	Log       *slog.Logger
	Now       func() time.Time

	WaitingEvery  time.Duration // flight entries not yet matched; default 3m
	TrackingEvery time.Duration // entries following an aircraft; default 45s
	MaxPosAge     time.Duration // ignore positions older than this; default 60s
	// StopoverGrace is how long a flight entry's aircraft may sit on the
	// ground before the flight counts as finished; default 3h.
	StopoverGrace time.Duration
	// ProviderEvery is the least time between flight-number lookups for one watch; each costs a request
	// against a quota. Default 30m.
	ProviderEvery time.Duration
	// ProviderMaxAge ignores a provider position older than this; default 6h.
	ProviderMaxAge time.Duration

	// TextSearchEvery is the least time between message searches for one flight watch; default 10m.
	TextSearchEvery time.Duration
	// TextMatchAge is how recent a message naming the flight must be to count: an aircraft in flight is
	// heard from every few minutes, while the same flight number yesterday was another aircraft. Default 3h.
	TextMatchAge time.Duration

	providerAt map[int64]time.Time
	searchAt   map[int64]time.Time
	last       map[int64]time.Time
	lookedUp   map[string]time.Time
	noted      map[string]time.Time
}

func (p *Poller) defaults() {
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Log == nil {
		p.Log = slog.Default()
	}
	if p.WaitingEvery == 0 {
		p.WaitingEvery = 3 * time.Minute
	}
	if p.TrackingEvery == 0 {
		p.TrackingEvery = 45 * time.Second
	}
	if p.MaxPosAge == 0 {
		p.MaxPosAge = 60 * time.Second
	}
	if p.StopoverGrace == 0 {
		p.StopoverGrace = 3 * time.Hour
	}
	if p.ProviderEvery == 0 {
		p.ProviderEvery = 30 * time.Minute
	}
	if p.ProviderMaxAge == 0 {
		p.ProviderMaxAge = 6 * time.Hour
	}
	if p.TextSearchEvery == 0 {
		p.TextSearchEvery = 10 * time.Minute
	}
	if p.TextMatchAge == 0 {
		p.TextMatchAge = 3 * time.Hour
	}
	if p.last == nil {
		p.searchAt = map[int64]time.Time{}
		p.providerAt = map[int64]time.Time{}
		p.last = map[int64]time.Time{}
		p.lookedUp = map[string]time.Time{}
		p.noted = map[string]time.Time{}
	}
}

// Run ticks until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	p.defaults()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		p.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Once expires finished entries and polls every entry that is due.
func (p *Poller) Once(ctx context.Context) {
	p.defaults()
	now := p.Now()
	if n, err := p.Store.ExpireWatches(now); err != nil {
		p.Log.Error("expire", "err", err)
	} else if n > 0 {
		p.Log.Info("expired watches", "n", n)
	}
	entries, err := p.Store.ListWatches(true)
	if err != nil {
		p.Log.Error("list watches", "err", err)
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !e.Active(now) {
			continue
		}
		every := p.TrackingEvery
		if e.Status == watchlist.StatusWaiting {
			every = p.WaitingEvery
		}
		if t, ok := p.last[e.ID]; ok && now.Sub(t) < every {
			continue
		}
		p.last[e.ID] = now
		if err := p.poll(ctx, e, now); err != nil {
			p.Log.Warn("poll failed", "watch", e.Ident, "err", err)
		}
	}
}

// poll queries one entry, latches/updates it, and records the position.
// Changes to the entry are written back once, on return.
func (p *Poller) poll(ctx context.Context, e watchlist.Entry, now time.Time) (err error) {
	changed := false
	defer func() {
		if changed {
			if uerr := p.Store.UpdateWatch(e); uerr != nil && err == nil {
				err = uerr
			}
		}
	}()

	if e.Kind == watchlist.KindTail && e.Tail == "" {
		e.Tail, changed = e.Ident, true
	}
	if e.Hex == "" && e.Kind == watchlist.KindTail {
		if hex := p.resolveHex(ctx, e.Ident, now); hex != "" {
			e.Hex, changed = hex, true
		}
	}
	if e.Tail == "" && e.Hex != "" {
		if t, ok := p.Store.TailForHex(e.Hex); ok {
			e.Tail, changed = t, true
		}
	}

	var ac []adsb.Aircraft
	switch {
	case e.Hex != "":
		ac, err = p.ADSB.Hex(ctx, e.Hex)
	case e.Kind == watchlist.KindTail:
		ac, err = p.ADSB.Reg(ctx, e.Ident)
	case e.Kind == watchlist.KindFlight:
		ac, err = p.findFlight(ctx, e)
	}
	if err != nil {
		return err
	}
	if len(ac) == 0 && e.Kind == watchlist.KindFlight {
		// No ADS-B: ask the flight-number provider, which also covers mid-ocean.
		heard, c := p.askProvider(ctx, &e, now)
		changed = changed || c
		if heard {
			return nil
		}
		if e.Hex == "" {
			changed = p.searchMessages(ctx, &e, now) || changed
		}
	}
	if len(ac) == 0 {
		// Not airborne, or out of coverage. A parked aircraft often drops off
		// the feed, so this is also how a finished flight ends.
		c1, c2 := p.checkStopover(&e, now), p.checkVanishedNearDest(&e, now)
		changed = c1 || c2 || changed
		return nil
	}
	a := ac[0]
	if e.Hex == "" {
		e.Hex, changed = a.Hex, true
	}
	if e.Tail == "" && a.Reg != "" {
		e.Tail, changed = strings.ToUpper(a.Reg), true
	}
	p.noteAirframe(a, now)

	cs := a.Callsign()
	if e.Kind == watchlist.KindFlight && e.Dest == "" && !e.LandedAt.IsZero() && cs != "" && cs != e.Callsign {
		// No destination given: the aircraft landed and is now operating
		// another flight number, so this flight is over. (With a destination
		// we wait to arrive there instead.)
		p.Log.Info("flight ended: callsign changed after landing", "watch", e.Ident, "was", e.Callsign, "now", cs)
		e.Status, changed = watchlist.StatusDone, true
		return nil
	}
	if cs != "" && cs != e.Callsign {
		e.Callsign, changed = cs, true
	}
	if e.Status == watchlist.StatusWaiting {
		e.Status, changed = watchlist.StatusTracking, true
		p.Log.Info("flight matched", "watch", e.Ident, "hex", e.Hex, "tail", e.Tail, "callsign", e.Callsign)
	}

	if e.Kind == watchlist.KindFlight {
		switch {
		case !a.OnGround():
			if !e.Airborne || !e.LandedAt.IsZero() {
				if !e.LandedAt.IsZero() {
					p.Log.Info("stopover over: airborne again", "watch", e.Ident)
				}
				e.Airborne, e.LandedAt, changed = true, time.Time{}, true
			}
		case e.Airborne && e.LandedAt.IsZero():
			p.Log.Info("landed", "watch", e.Ident, "grace", p.StopoverGrace)
			e.LandedAt, changed = now, true
		}
		if p.atDestination(e, a) {
			p.Log.Info("flight ended: landed at destination", "watch", e.Ident, "dest", e.Dest)
			e.Status, changed = watchlist.StatusDone, true
		}
		changed = p.checkStopover(&e, now) || changed
	}

	if !a.HasPosition(p.MaxPosAge) {
		return nil
	}
	pos := store.Position{
		Hex: a.Hex, T: now.Add(-seconds(a.SeenPos)), Lat: *a.Lat, Lon: *a.Lon,
		AltFt: a.AltBaro.Feet, OnGround: a.AltBaro.OnGround, GS: a.GS, Track: a.Track,
		VRate: a.BaroRate, Callsign: cs, Reg: strings.ToUpper(a.Reg), Source: a.Source,
	}
	_, err = p.Store.AddPosition(pos)
	return err
}

// Arrival detection at a given destination.
const (
	// ArrivalRadiusNM is how close to the airport reference point an aircraft
	// on the ground must be to count as having arrived (big airports have
	// runways several miles from the reference point).
	ArrivalRadiusNM = 10.0
	// An aircraft last seen below ApproachAltFt within ApproachRadiusNM of the
	// destination, then silent for VanishedAfter, is taken to have landed
	// (many arrivals drop out of ADS-B coverage on the ground).
	ApproachRadiusNM = 15.0
	ApproachAltFt    = 5000.0
	VanishedAfter    = 10 * time.Minute
	// With a destination, a landing elsewhere is a stopover or diversion; the
	// watch ends only after DiversionFactor x StopoverGrace on the ground.
	DiversionFactor = 4
)

// atDestination reports whether a is on the ground at the entry's destination.
func (p *Poller) atDestination(e watchlist.Entry, a adsb.Aircraft) bool {
	dest, ok := airports.Lookup(e.Dest)
	if !ok || !e.Airborne || !a.OnGround() || !a.HasPosition(p.MaxPosAge) {
		return false
	}
	return geo.DistNM(*a.Lat, *a.Lon, dest.Lat, dest.Lon) <= ArrivalRadiusNM
}

// checkVanishedNearDest ends a flight whose aircraft was last seen low and
// close to the destination and has not been seen since.
func (p *Poller) checkVanishedNearDest(e *watchlist.Entry, now time.Time) bool {
	dest, ok := airports.Lookup(e.Dest)
	if !ok || e.Kind != watchlist.KindFlight || e.Status != watchlist.StatusTracking || !e.Airborne || e.Hex == "" {
		return false
	}
	last, ok := p.Store.LastPosition(e.Hex)
	if !ok || now.Sub(last.T) < VanishedAfter || last.AltFt == nil || *last.AltFt > ApproachAltFt {
		return false
	}
	if geo.DistNM(last.Lat, last.Lon, dest.Lat, dest.Lon) > ApproachRadiusNM {
		return false
	}
	p.Log.Info("flight ended: lost on approach to destination", "watch", e.Ident, "dest", e.Dest, "last_seen", last.T)
	e.Status = watchlist.StatusDone
	return true
}

// checkStopover ends a flight entry that has been on the ground too long.
// Without a destination, ADS-B gives no way to tell the end of a flight from
// an intermediate stop, so StopoverGrace decides. With one, only a much longer
// stay (a diversion) ends it. It reports whether it changed the entry.
func (p *Poller) checkStopover(e *watchlist.Entry, now time.Time) bool {
	grace := p.StopoverGrace
	if e.Dest != "" {
		grace *= DiversionFactor
	}
	if e.Kind != watchlist.KindFlight || e.Status != watchlist.StatusTracking ||
		e.LandedAt.IsZero() || now.Sub(e.LandedAt) < grace {
		return false
	}
	p.Log.Info("flight ended: on the ground past the stopover grace", "watch", e.Ident, "landed", e.LandedAt, "grace", grace)
	e.Status = watchlist.StatusDone
	return true
}

// resolveHex finds the hex for a registration from the permanent table, else
// from airframes.io (at most once an hour per registration), and remembers it.
func (p *Poller) resolveHex(ctx context.Context, tail string, now time.Time) string {
	if hex, ok := p.Store.HexForTail(tail); ok {
		return hex
	}
	if p.Airframes == nil || now.Sub(p.lookedUp[tail]) < time.Hour {
		return ""
	}
	p.lookedUp[tail] = now
	af, err := p.Airframes.ByTail(ctx, tail)
	if err != nil {
		p.Log.Warn("airframe lookup failed", "tail", tail, "err", err)
		return ""
	}
	if af == nil {
		p.Log.Info("airframes.io does not know this registration", "tail", tail)
		return ""
	}
	if err := p.Store.RecordAirframe(store.Airframe{Hex: af.ICAO, Tail: af.Tail, Type: af.Type, Source: "airframes.io", LastSeen: now}); err != nil {
		p.Log.Error("record airframe", "err", err)
	}
	return af.ICAO
}

// noteAirframe records a sighting's hex/tail pairing, at most hourly per pair.
func (p *Poller) noteAirframe(a adsb.Aircraft, now time.Time) {
	tail := strings.ToUpper(a.Reg)
	key := a.Hex + "/" + tail
	if a.Hex == "" || tail == "" || now.Sub(p.noted[key]) < time.Hour {
		return
	}
	p.noted[key] = now
	if err := p.Store.RecordAirframe(store.Airframe{Hex: a.Hex, Tail: tail, Type: a.Type, Source: a.Source, LastSeen: now}); err != nil {
		p.Log.Error("record airframe", "err", err)
	}
}

func seconds(f *float64) time.Duration {
	if f == nil {
		return 0
	}
	return time.Duration(*f * float64(time.Second))
}

// findFlight looks for the flight's callsign variants and returns the first hit.
func (p *Poller) findFlight(ctx context.Context, e watchlist.Entry) ([]adsb.Aircraft, error) {
	cands, err := CallsignCandidates(e.Ident)
	if err != nil {
		return nil, err
	}
	for _, cs := range cands {
		ac, err := p.ADSB.Callsign(ctx, cs)
		if err != nil {
			return nil, err
		}
		if len(ac) > 0 {
			return ac, nil
		}
	}
	return nil, nil
}

var callsignShape = regexp.MustCompile(`^[A-Z]{3}\d{1,4}[A-Z]?$`)

// askProvider looks the flight up by number when ADS-B has nothing. If the provider has it in the air
// it latches the aircraft onto the watch (hex, tail, callsign, destination if none was given) so its
// datalink messages are followed, and stores the provider's position. heard says the provider has
// the flight airborne, so the watch should not be treated as landed or lost; changed says e was altered.
func (p *Poller) askProvider(ctx context.Context, e *watchlist.Entry, now time.Time) (heard, changed bool) {
	if p.Flights == nil || now.Sub(p.providerAt[e.ID]) < p.ProviderEvery {
		return false, false
	}
	_, iata := airlines.Forms(e.Ident)
	if iata == "" && callsignShape.MatchString(e.Ident) {
		iata = e.Ident // an airline missing from our table: the provider also takes the ICAO callsign (FJI911)
	}
	if iata == "" {
		return false, false
	}
	p.providerAt[e.ID] = now
	dates := []string{e.Date}
	if e.Date == "" { // the day it left, which may be yesterday for a long flight
		dates = []string{now.UTC().Format("2006-01-02"), now.UTC().AddDate(0, 0, -1).Format("2006-01-02")}
	}
	var f *aerodatabox.Flight
	for _, d := range dates {
		fs, err := p.Flights.ByNumber(ctx, iata, d)
		if err != nil {
			p.Log.Warn("flight lookup failed", "watch", e.Ident, "err", err)
			return false, false
		}
		for i := range fs {
			if fs[i].Airborne() {
				f = &fs[i]
				break
			}
		}
		if f != nil {
			break
		}
	}
	if f == nil {
		return false, false
	}
	if e.Hex != "" && f.Hex != "" && f.Hex != e.Hex {
		p.Log.Info("flight lookup found a different aircraft; ignoring", "watch", e.Ident, "have", e.Hex, "provider", f.Hex)
		return false, false
	}
	if e.Hex == "" && f.Hex != "" {
		e.Hex, changed = f.Hex, true
		if e.Tail == "" && f.Reg != "" {
			e.Tail = f.Reg
		}
		if f.Callsign != "" {
			e.Callsign = f.Callsign
		}
		if err := p.Store.RecordAirframe(store.Airframe{Hex: f.Hex, Tail: f.Reg, Source: aerodatabox.Source, LastSeen: now}); err != nil {
			p.Log.Error("record airframe", "err", err)
		}
		p.Log.Info("flight found by number", "watch", e.Ident, "hex", f.Hex, "tail", f.Reg, "from", f.From, "to", f.To)
	}
	if e.Dest == "" && f.To != "" {
		e.Dest, changed = f.To, true
	}
	if e.Status == watchlist.StatusWaiting && e.Hex != "" {
		e.Status, changed = watchlist.StatusTracking, true
	}
	if !e.Airborne {
		e.Airborne, e.LandedAt, changed = true, time.Time{}, true
	}
	if f.HasPos && f.Hex != "" && now.Sub(f.PosAt) <= p.ProviderMaxAge {
		_, err := p.Store.AddPosition(store.Position{Hex: f.Hex, T: f.PosAt, Lat: f.Lat, Lon: f.Lon,
			GS: f.GS, Track: f.Track, Callsign: f.Callsign, Reg: f.Reg, Source: aerodatabox.Source})
		if err != nil {
			p.Log.Error("record provider position", "err", err)
		}
	}
	return true, changed
}

// searchMessages is the last resort for a flight watch with no aircraft: look for the flight number in the
// text of recent datalink messages, which name the airframe. It takes the aircraft of the newest message that
// is recent enough to be this flight, and follows it by hex from then on. changed says e was altered.
func (p *Poller) searchMessages(ctx context.Context, e *watchlist.Entry, now time.Time) (changed bool) {
	if p.Search == nil || e.Kind != watchlist.KindFlight || now.Sub(p.searchAt[e.ID]) < p.TextSearchEvery {
		return false
	}
	icao, _ := airlines.Forms(e.Ident)
	if icao == "" {
		icao = strings.ToUpper(e.Ident)
	}
	p.searchAt[e.ID] = now
	ms, err := p.Search.SearchText(ctx, icao)
	if err != nil {
		p.Log.Warn("message search failed", "watch", e.Ident, "err", err)
		return false
	}
	hex, tail, ok := newestNaming(ms, icao, now, p.TextMatchAge)
	if !ok {
		return false
	}
	e.Hex, changed = hex, true
	if e.Tail == "" {
		e.Tail = tail
	}
	if e.Callsign == "" {
		e.Callsign = icao
	}
	if e.Status == watchlist.StatusWaiting {
		e.Status = watchlist.StatusTracking
	}
	if tail != "" {
		if err := p.Store.RecordAirframe(store.Airframe{Hex: hex, Tail: tail, Source: "airframes.io", LastSeen: now}); err != nil {
			p.Log.Error("record airframe", "err", err)
		}
	}
	p.Log.Info("flight found in datalink messages", "watch", e.Ident, "hex", hex, "tail", tail)
	return changed
}

// newestNaming returns the airframe of the newest message, no older than maxAge, whose text names the
// callsign: the callsign must not run on into more digits (UPS58 is not UPS580) and the message must
// say which airframe it came from.
func newestNaming(ms []airframes.Message, callsign string, now time.Time, maxAge time.Duration) (hex, tail string, ok bool) {
	var best time.Time
	for _, m := range ms {
		h := strings.ToUpper(string(m.Airframe.ICAO))
		if h == "" || m.Timestamp.Before(now.Add(-maxAge)) || m.Timestamp.After(now.Add(5*time.Minute)) || !m.Timestamp.After(best) {
			continue
		}
		if !namesCallsign(string(m.Text), callsign) {
			continue
		}
		best, hex, tail, ok = m.Timestamp, h, strings.ToUpper(string(m.Airframe.Tail)), true
	}
	return
}

func namesCallsign(text, callsign string) bool {
	text = strings.ToUpper(text)
	for i := 0; ; {
		j := strings.Index(text[i:], callsign)
		if j < 0 {
			return false
		}
		end := i + j + len(callsign)
		if end >= len(text) || text[end] < '0' || text[end] > '9' {
			return true
		}
		i = i + j + 1
	}
}
