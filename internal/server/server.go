// Package server serves the JSON API and the static UI.
package server

import (
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/avanha/fullflight/internal/aerodatabox"
	"github.com/avanha/fullflight/internal/airlines"
	"github.com/avanha/fullflight/internal/airports"
	"github.com/avanha/fullflight/internal/flights"
	"github.com/avanha/fullflight/internal/history"
	"github.com/avanha/fullflight/internal/landing"
	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/regs"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
	"github.com/avanha/fullflight/internal/waypoints"
)

//go:embed static
var static embed.FS

type Server struct {
	Store       *store.Store
	Nav         *navdata.DB // optional waypoint database
	Username    string
	Password    string // empty disables auth (config only allows that on loopback)
	Now         func() time.Time
	Backfill    *history.Manager // nil when backfill is off in the config
	StationList StationSource    // airframes.io's list of receiving stations; nil if there is none

	flightMu    sync.Mutex
	flightCache map[string]cachedFlight // hex -> the aircraft's last flight, for the airframes page
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/watches", s.listWatches)
	mux.HandleFunc("POST /api/watches", s.addWatch)
	mux.HandleFunc("DELETE /api/watches/{id}", s.deleteWatch)
	mux.HandleFunc("PATCH /api/watches/{id}", s.patchWatch)
	mux.HandleFunc("GET /api/track/{hex}", s.track)
	mux.HandleFunc("GET /api/airframes", s.airframes)
	mux.HandleFunc("GET /api/airframes/{hex}/notes", s.listNotes)
	mux.HandleFunc("POST /api/airframes/{hex}/notes", s.addNote)
	mux.HandleFunc("PUT /api/airframes/{hex}/notes/{id}", s.editNote)
	mux.HandleFunc("DELETE /api/airframes/{hex}/notes/{id}", s.deleteNote)
	mux.HandleFunc("GET /api/messages/{hex}", s.messages)
	mux.HandleFunc("GET /api/flights/{hex}", s.flights)
	mux.HandleFunc("PUT /api/flights/{hex}/route", s.setFlightRoute)
	mux.HandleFunc("PUT /api/flights/{hex}/join", s.setFlightJoin)
	mux.HandleFunc("GET /api/plan/{hex}", s.plans)
	mux.HandleFunc("GET /api/fixes", s.fixes)
	mux.HandleFunc("GET /api/airports", s.airports)
	mux.HandleFunc("GET /api/stations", s.listStations)
	mux.HandleFunc("POST /api/stations/refresh", s.refreshStations)
	mux.HandleFunc("GET /api/backfill", s.backfillStatus)
	mux.HandleFunc("GET /api/backfill/plan", s.backfillPlan)
	mux.HandleFunc("POST /api/backfill", s.backfillStart)
	mux.HandleFunc("DELETE /api/backfill", s.backfillCancel)
	return s.auth(mux)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.Password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(s.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="FullFlight"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

type watchJSON struct {
	ID       int64        `json:"id"`
	Kind     string       `json:"kind"`
	Ident    string       `json:"ident"`
	Date     string       `json:"date,omitempty"`
	Dest     string       `json:"dest,omitempty"` // ICAO code
	DestName string       `json:"dest_name,omitempty"`
	DestLat  *float64     `json:"dest_lat,omitempty"` // for stopping a projection at the destination
	DestLon  *float64     `json:"dest_lon,omitempty"`
	Start    time.Time    `json:"start"`
	End      time.Time    `json:"end"`
	Status   string       `json:"status"`
	Hex      string       `json:"hex,omitempty"`
	Tail     string       `json:"tail,omitempty"`
	Callsign string       `json:"callsign,omitempty"`
	Type     string       `json:"type,omitempty"` // aircraft type, e.g. A359
	LastPos  *lastPos     `json:"last_pos,omitempty"`
	Route    *routeJSON   `json:"route,omitempty"`
	Flight   *flightJSON  `json:"flight,omitempty"`      // tail and hex watches only: the flight it is operating
	Poll     *pollJSON    `json:"poll,omitempty"`        // what the last ADS-B ask for it found
	Hold     *holdJSON    `json:"lookup_hold,omitempty"` // a flight watch: the flight-number lookup is paused, because the provider refused us
	Landing  *landingJSON `json:"landing,omitempty"`     // the aircraft has probably landed: its last fix was low, near an airport, and nothing followed
	Notes    int          `json:"notes,omitempty"`       // notes you have written about this airframe
	LastNote string       `json:"last_note,omitempty"`   // the newest, abbreviated, for a hover
}

type holdJSON struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// pollJSON is what the last ADS-B ask for a watch found, so the page can say why there is, or is not, a track.
type pollJSON struct {
	Asked   time.Time  `json:"asked"`
	Outcome string     `json:"outcome"` // heard, not_seen, no_position or error
	Detail  string     `json:"detail,omitempty"`
	Heard   *time.Time `json:"heard,omitempty"` // when the newest position it ever gave was taken
	Source  string     `json:"source,omitempty"`
	Misses  int        `json:"misses,omitempty"` // asks since then that did not find it
}

// landingJSON is an inferred landing (see package landing).
type landingJSON struct {
	Code   string    `json:"code"`
	Name   string    `json:"name"`
	Lat    float64   `json:"lat"`
	Lon    float64   `json:"lon"`
	At     time.Time `json:"at"` // when it probably touched down
	DistNM float64   `json:"dist_nm"`
	Basis  string    `json:"basis"`
	Next   string    `json:"next,omitempty"` // the callsign it has taken on since landing: the next flight, which has not departed
}

// landing says whether the aircraft's newest position, from any source, is where it landed. A later
// datalink position (ADS-C, a periodic report) means it did not.
func (s *Server) landing(hex, dest string) *landingJSON {
	p, ok := s.Store.LastPosition(hex)
	if !ok {
		return nil
	}
	if m, ok := s.Store.LastMessagePosition(hex); ok && m.T.After(p.T) {
		return nil
	}
	l := landing.Infer(landing.Fix{T: p.T, Lat: p.Lat, Lon: p.Lon, AltFt: p.AltFt, GS: p.GS, Track: p.Track, VRate: p.VRate, OnGround: p.OnGround}, s.now(), dest)
	if l == nil {
		return nil
	}
	code := l.Airport.ICAO
	if code == "" {
		code = l.Airport.IATA
	}
	out := &landingJSON{Code: code, Name: l.Airport.Name, Lat: l.Airport.Lat, Lon: l.Airport.Lon, At: l.At, DistNM: l.DistNM, Basis: l.Basis}
	if p.OnGround {
		// The newest fix is only the latest of many on the ground: the landing was when the stay began, and a different
		// callsign since then is the aircraft's next flight, still to leave.
		if since, ok := s.Store.OnGroundSince(hex); ok {
			out.At = since
		}
		flown, now := airlineForm(s.Store.LastAirborneCallsign(hex)), airlineForm(p.Callsign)
		if flown != "" && now != "" && flown != now {
			out.Next = now
		}
	}
	return out
}

// airlineForm is a callsign in its ICAO form, as it is written for comparison.
func airlineForm(id string) string {
	if icao, _ := airlines.Forms(id); icao != "" {
		return icao
	}
	return strings.ToUpper(strings.TrimSpace(id))
}

// flightJSON is the flight an aircraft is currently operating, from its newest ADS-B callsign
// or the flight number in its newest message, whichever is later.
type flightJSON struct {
	Callsign string    `json:"callsign,omitempty"` // ICAO form, e.g. CPA113
	Number   string    `json:"number,omitempty"`   // IATA form, e.g. CX113
	T        time.Time `json:"t"`
	Source   string    `json:"source"` // "ADS-B" or "messages"
	Stale    bool      `json:"stale,omitempty"`
}

func (s *Server) flight(hex string) *flightJSON {
	var id, src string
	var at time.Time
	if cs, t, ok := s.Store.LastCallsign(hex); ok {
		id, at, src = cs, t, "ADS-B"
	}
	if f, ok := s.Store.LastFlight(hex); ok && f.T.After(at) {
		id, at, src = firstNonEmpty(f.Callsign, f.Flight), f.T, "messages"
	}
	if id == "" {
		return nil
	}
	age := s.now().Sub(at)
	if age > InfoHideAfter {
		return nil
	}
	out := &flightJSON{T: at, Source: src, Stale: age > InfoFreshFor}
	out.Callsign, out.Number = airlines.Forms(id)
	if out.Callsign == "" && out.Number == "" {
		out.Callsign = strings.ToUpper(id) // an airline we have no table entry for: show it as received
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// How long something the aircraft says about itself (its destination, its flight) stays
// believable. Reports go out every ~20 minutes in flight, so one older than InfoFreshFor means
// we have lost contact, or the aircraft has landed and moved on to another sector. After
// InfoHideAfter we stop showing it at all.
const (
	InfoFreshFor = 90 * time.Minute
	// PlanFreshFor is how long a filed plan still names the flight's airports: a long-haul flight lasts most of a day.
	PlanFreshFor  = 20 * time.Hour
	InfoHideAfter = 24 * time.Hour
)

// routeJSON is the origin and destination the aircraft last reported (REP081, ACMF, an airline report or a flight plan).
type routeJSON struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	FromName string    `json:"from_name,omitempty"`
	ToName   string    `json:"to_name,omitempty"`
	ToLat    *float64  `json:"to_lat,omitempty"`
	ToLon    *float64  `json:"to_lon,omitempty"`
	T        time.Time `json:"t"`
	Stale    bool      `json:"stale,omitempty"` // older than InfoFreshFor: may be out of date
	Source   string    `json:"source"`
}

func (s *Server) route(hex string) *routeJSON {
	r, ok := s.Store.LastRouteAt(hex, s.now())
	if !ok {
		return nil
	}
	age := s.now().Sub(r.T)
	if age > InfoHideAfter {
		return nil
	}
	out := &routeJSON{From: r.From, To: r.To, T: r.T, Stale: r.Source != "flight plan" && age > InfoFreshFor || r.Source == "flight plan" && age > PlanFreshFor, Source: r.Source}
	if a, ok := airports.Lookup(r.From); ok {
		out.FromName = a.Name
	}
	if a, ok := airports.Lookup(r.To); ok {
		lat, lon := a.Lat, a.Lon
		out.ToName, out.ToLat, out.ToLon = a.Name, &lat, &lon
	}
	return out
}

// lastPos is the newest position we hold for a watched aircraft, from ADS-B or
// from a datalink message, whichever is later.
type lastPos struct {
	T     time.Time `json:"t"`
	Kind  string    `json:"kind"` // ADS-B, ADS-C, CPDLC or REP081
	Lat   float64   `json:"lat"`
	Lon   float64   `json:"lon"`
	AltFt *float64  `json:"alt_ft,omitempty"`
}

func (s *Server) lastPosition(hex string) *lastPos {
	var best *lastPos
	if p, ok := s.Store.LastPosition(hex); ok {
		best = &lastPos{T: p.T, Kind: "ADS-B", Lat: p.Lat, Lon: p.Lon, AltFt: p.AltFt}
	}
	if m, ok := s.Store.LastMessagePosition(hex); ok && (best == nil || m.T.After(best.T)) {
		kind := map[string]string{"adsc": "ADS-C", "cpdlc": "CPDLC", "miam": "REP081", "fms_position": "FMS", "acmf_report": "ACMF", "aoc_position": "AOC", "pos_report": "POS", "posrpt": "POSRPT", "posa1": "POSA1", "sbpos": "SB", "ops_report": "Airline"}[m.Kind]
		if kind == "" {
			kind = m.Kind
		}
		best = &lastPos{T: m.T, Kind: kind, Lat: m.Lat, Lon: m.Lon, AltFt: m.AltFt}
	}
	return best
}

func toJSON(e watchlist.Entry) watchJSON {
	w := watchJSON{ID: e.ID, Kind: string(e.Kind), Ident: e.Ident, Date: e.Date, Dest: e.Dest, Start: e.Start, End: e.End,
		Status: string(e.Status), Hex: e.Hex, Tail: e.Tail, Callsign: e.Callsign}
	if a, ok := airports.Lookup(e.Dest); ok {
		lat, lon := a.Lat, a.Lon
		w.DestName, w.DestLat, w.DestLon = a.Name, &lat, &lon
	}
	return w
}

// withAircraft adds what we know about the followed aircraft.
func (s *Server) withAircraft(j watchJSON) watchJSON {
	if p, ok := s.Store.PollStateFor(j.ID); ok {
		pj := &pollJSON{Asked: p.Asked, Outcome: p.Outcome, Detail: p.Detail, Source: p.Source, Misses: p.Misses}
		if !p.Heard.IsZero() {
			h := p.Heard
			pj.Heard = &h
		}
		j.Poll = pj
	}
	if j.Kind == string(watchlist.KindFlight) {
		if until, why, ok := s.Store.ProviderHold(aerodatabox.Source, s.now()); ok {
			j.Hold = &holdJSON{Until: until, Reason: why}
		}
	}
	if j.Hex != "" {
		j.Type = s.Store.TypeForHex(j.Hex)
		j.LastPos = s.lastPosition(j.Hex)
		j.Route = s.route(j.Hex)
		dest := j.Dest
		if dest == "" && j.Route != nil && !j.Route.Stale {
			dest = j.Route.To
		}
		j.Landing = s.landing(j.Hex, dest)
		if n, latest := s.Store.NoteSummary(j.Hex); n > 0 {
			j.Notes, j.LastNote = n, preview(latest)
		}
		if j.Kind != string(watchlist.KindFlight) { // a flight watch already is a flight
			j.Flight = s.flight(j.Hex)
		}
	}
	return j
}

func (s *Server) listWatches(w http.ResponseWriter, r *http.Request) {
	es, err := s.Store.ListWatches(false)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]watchJSON, 0, len(es))
	for _, e := range es {
		out = append(out, s.withAircraft(toJSON(e)))
	}
	writeJSON(w, 200, out)
}

// addWatch takes {"input": "UA869 2026-10-31 SFO"} or {"input": "B-LRM", "until": "<RFC3339>"}.
var hexRe = regexp.MustCompile(`^[0-9A-F]{6}$`)

func (s *Server) addWatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input string `json:"input"`
		Until string `json:"until"`
		Hex   string `json:"hex"` // the address of a registration already known, so a tail watch need not look it up
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		fail(w, 400, "bad JSON")
		return
	}
	var until time.Time
	if in.Until != "" {
		var err error
		if until, err = time.Parse(time.RFC3339, in.Until); err != nil {
			fail(w, 400, "until must be RFC 3339, e.g. 2026-11-02T00:00:00Z")
			return
		}
	}
	e, err := watchlist.Parse(in.Input, s.now(), until)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if hex := strings.ToUpper(in.Hex); e.Kind == watchlist.KindTail && e.Hex == "" && hexRe.MatchString(hex) {
		e.Hex = hex
	}
	if e.Kind == watchlist.KindFlight {
		// a flight we could never find on the air (an airline code we cannot turn into a callsign) is refused now,
		// not left to fail on every poll
		if _, err := airlines.CallsignCandidates(e.Ident); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	if e.Dest != "" {
		a, ok := airports.Lookup(e.Dest)
		if !ok {
			fail(w, 400, "unknown airport "+strconv.Quote(e.Dest))
			return
		}
		e.Dest = a.ICAO
	}
	e, err = s.Store.AddWatch(e)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, s.withAircraft(toJSON(e)))
}

// patchWatch pauses or resumes a watch: {"paused": true} stops polling for it while keeping it, and everything
// stored about its aircraft, in the list; {"paused": false} carries on.
func (s *Server) patchWatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	var in struct {
		Paused *bool `json:"paused"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Paused == nil {
		fail(w, 400, `expected {"paused": true} or {"paused": false}`)
		return
	}
	e, err := s.Store.GetWatch(id)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "no such watch")
		return
	} else if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if *in.Paused {
		err = e.Pause()
	} else {
		err = e.Resume(s.now())
	}
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if err := s.Store.UpdateWatch(e); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.withAircraft(toJSON(e)))
}

func (s *Server) deleteWatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	if err := s.Store.DeleteWatch(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pointJSON struct {
	T        time.Time `json:"t"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	AltFt    *float64  `json:"alt_ft"`
	GS       *float64  `json:"gs"`
	Track    *float64  `json:"track"`
	VRate    *float64  `json:"vrate"` // ft/min, positive climbing
	OnGround bool      `json:"on_ground,omitempty"`
	Callsign string    `json:"callsign"`
	Source   string    `json:"source"`
}

// track returns the positions of one aircraft, default the last 48 hours.
func (s *Server) track(w http.ResponseWriter, r *http.Request) {
	hex := strings.ToUpper(r.PathValue("hex"))
	since, until := s.window(r, 48, 24*14)
	ps, err := s.Store.Track(hex, since, until)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]pointJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, pointJSON{T: p.T, Lat: p.Lat, Lon: p.Lon, AltFt: p.AltFt, GS: p.GS, Track: p.Track, VRate: p.VRate, OnGround: p.OnGround, Callsign: p.Callsign, Source: p.Source})
	}
	writeJSON(w, 200, out)
}

// airframeJSON is one aircraft as the airframes page shows it.
type airframeJSON struct {
	Hex          string          `json:"hex"`
	Tail         string          `json:"tail,omitempty"` // the registration it was last seen with
	Tails        []tailSeenJSON  `json:"tails,omitempty"`
	Type         string          `json:"type,omitempty"`
	FirstSeen    time.Time       `json:"first_seen,omitzero"`
	LastSeen     time.Time       `json:"last_seen,omitzero"` // the newest ADS-B position or datalink message
	LastADSB     time.Time       `json:"last_adsb,omitzero"`
	LastDatalink time.Time       `json:"last_datalink,omitzero"`
	Watched      bool            `json:"watched,omitempty"`
	Notes        int             `json:"notes"`
	LastNote     string          `json:"last_note,omitempty"`
	Messages     int             `json:"messages"`
	Positions    int             `json:"positions"`
	LastFlight   *lastFlightJSON `json:"last_flight,omitempty"`
}

// lastFlightJSON is the newest flight we have worked out for an aircraft: its callsign and flight number, and the
// route and departure time where we know them.
type lastFlightJSON struct {
	Flight      string    `json:"flight,omitempty"`
	Callsign    string    `json:"callsign,omitempty"`
	From        string    `json:"from,omitempty"`
	To          string    `json:"to,omitempty"`
	FromManual  bool      `json:"from_manual,omitempty"` // entered by hand: see flights.Segment
	FromSeen    bool      `json:"from_seen,omitempty"`   // where ADS-B saw it climb out: see flights.Segment
	ToManual    bool      `json:"to_manual,omitempty"`
	FromName    string    `json:"from_name,omitempty"`
	ToName      string    `json:"to_name,omitempty"`
	Departed    time.Time `json:"departed,omitzero"`
	DepartedHow string    `json:"departed_how,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"` // the last we heard of it on that flight
}

type cachedFlight struct {
	at time.Time
	v  *lastFlightJSON
}

// lastFlightFor is the aircraft's newest identified flight in the last few days (else its newest flight of any kind), or
// nil. Worked out the way the map's flights are, and kept for a minute because the airframes page asks every 30 seconds.
func (s *Server) lastFlightFor(hex string, now time.Time) *lastFlightJSON {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	if c, ok := s.flightCache[hex]; ok && now.Sub(c.at) < time.Minute {
		return c.v
	}
	var out *lastFlightJSON
	if segs, err := flights.Segments(s.Store, hex, now.Add(-lastFlightWindow), now.Add(time.Minute)); err == nil && len(segs) > 0 {
		sg := segs[len(segs)-1]
		for i := len(segs) - 1; i >= 0; i-- {
			if segs[i].Identified {
				sg = segs[i]
				break
			}
		}
		out = &lastFlightJSON{Flight: sg.Flight, Callsign: sg.Callsign, From: sg.From, To: sg.To, FromManual: sg.FromManual, FromSeen: sg.FromSeen, ToManual: sg.ToManual, Departed: sg.Departed, DepartedHow: sg.DepartedHow, Start: sg.Start, End: sg.End}
		if a, ok := airports.Lookup(sg.From); ok {
			out.FromName = a.Name
		}
		if a, ok := airports.Lookup(sg.To); ok {
			out.ToName = a.Name
		}
	}
	if s.flightCache == nil {
		s.flightCache = map[string]cachedFlight{}
	}
	s.flightCache[hex] = cachedFlight{now, out}
	return out
}

// lastFlightWindow is how far back the airframes page looks for an aircraft's last flight.
const lastFlightWindow = 7 * 24 * time.Hour

type tailSeenJSON struct {
	Tail      string    `json:"tail"`
	Type      string    `json:"type,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// airframes lists every aircraft we know of, most recently seen first.
func (s *Server) airframes(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.KnownAirframes()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]airframeJSON, 0, len(as))
	for _, a := range as {
		j := airframeJSON{Hex: a.Hex, Type: a.Type, FirstSeen: a.FirstSeen, LastSeen: a.LastSeen, LastADSB: a.LastADSB, LastDatalink: a.LastDatalink, Watched: a.Watched,
			Notes: a.Notes, LastNote: preview(a.LastNote), Messages: a.Messages, Positions: a.Positions}
		for _, t := range a.Tails {
			j.Tails = append(j.Tails, tailSeenJSON{t.Tail, t.Type, t.FirstSeen, t.LastSeen})
		}
		if now := s.now(); now.Sub(a.LastSeen) < lastFlightWindow {
			j.LastFlight = s.lastFlightFor(a.Hex, now)
		}
		if len(a.Tails) > 0 {
			j.Tail = a.Tails[0].Tail
		} else if t, ok := regs.TailForHex(a.Hex); ok {
			j.Tail = t // never seen with a registration, but its address gives it away
		}
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}

// preview is the start of a note, for a hover.
func preview(s string) string {
	const max = 200
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "\u2026"
	}
	return s
}

type noteJSON struct {
	ID      int64     `json:"id"`
	Hex     string    `json:"hex"`
	Note    string    `json:"note"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

func toNoteJSON(n store.Note) noteJSON {
	return noteJSON{n.ID, n.Hex, n.Text, n.Created, n.Updated}
}

var hexPath = regexp.MustCompile(`^[0-9A-F]{6}$`)

// noteHex reads the {hex} of a notes URL, or answers 400.
func noteHex(w http.ResponseWriter, r *http.Request) (string, bool) {
	h := strings.ToUpper(r.PathValue("hex"))
	if !hexPath.MatchString(h) {
		fail(w, 400, "an airframe is six hex digits, e.g. 780AB8")
		return "", false
	}
	return h, true
}

func noteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoNote):
		fail(w, 404, err.Error())
	case errors.Is(err, store.ErrEmptyNote), errors.Is(err, store.ErrLongNote):
		fail(w, 400, err.Error())
	default:
		fail(w, 500, err.Error())
	}
}

func readNote(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*store.MaxNoteBytes)).Decode(&in); err != nil {
		fail(w, 400, "bad JSON")
		return "", false
	}
	return in.Note, true
}

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	hex, ok := noteHex(w, r)
	if !ok {
		return
	}
	ns, err := s.Store.Notes(hex)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]noteJSON, 0, len(ns))
	for _, n := range ns {
		out = append(out, toNoteJSON(n))
	}
	writeJSON(w, 200, out)
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	hex, ok := noteHex(w, r)
	if !ok {
		return
	}
	text, ok := readNote(w, r)
	if !ok {
		return
	}
	n, err := s.Store.AddNote(hex, text, s.now())
	if err != nil {
		noteError(w, err)
		return
	}
	writeJSON(w, 201, toNoteJSON(n))
}

func (s *Server) editNote(w http.ResponseWriter, r *http.Request) {
	hex, ok := noteHex(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad note id")
		return
	}
	text, ok := readNote(w, r)
	if !ok {
		return
	}
	n, err := s.Store.UpdateNote(hex, id, text, s.now())
	if err != nil {
		noteError(w, err)
		return
	}
	writeJSON(w, 200, toNoteJSON(n))
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	hex, ok := noteHex(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad note id")
		return
	}
	if err := s.Store.DeleteNote(hex, id); err != nil {
		noteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type elsewhereJSON struct {
	Flight string `json:"flight"` // as the message was tagged, e.g. CX0301
	ID     string `json:"id"`     // that flight's segment
}

type messageJSON struct {
	ID             int64          `json:"id"`
	T              time.Time      `json:"t"`
	Label          string         `json:"label"`
	Kind           string         `json:"kind"`
	Direction      string         `json:"dir,omitempty"`
	Summary        string         `json:"summary"`
	Undecoded      bool           `json:"undecoded,omitempty"` // we have no decoder for it (vendor data, MIAM fragments)
	Detail         string         `json:"detail,omitempty"`
	Raw            string         `json:"raw"`
	Tail           string         `json:"tail,omitempty"`
	Flight         string         `json:"flight,omitempty"`
	Callsign       string         `json:"callsign,omitempty"`
	Station        string         `json:"station,omitempty"`
	RxCount        int            `json:"rx_count"`
	Lat            *float64       `json:"lat,omitempty"`
	Lon            *float64       `json:"lon,omitempty"`
	AltFt          *float64       `json:"alt_ft,omitempty"`
	PosT           *time.Time     `json:"pos_t,omitempty"`
	Elsewhere      *elsewhereJSON `json:"elsewhere,omitempty"` // airframes.io tagged it with another flight of this aircraft, which we have: it probably belongs there
	At             *time.Time     `json:"at,omitempty"`        // when it counts for its flight, if not when it arrived: the aircraft's own time on a message delivered late
	AtBasis        string         `json:"at_basis,omitempty"`  // and why: "event" (the time it gives for itself) or "scheduled" (its flight is due to leave then)
	Suspect        string         `json:"suspect,omitempty"`
	TimeNote       string         `json:"time_note,omitempty"`
	TimeUnverified bool           `json:"time_unverified,omitempty"`
	Mismatch       string         `json:"mismatch,omitempty"`
	AFTail         string         `json:"af_tail,omitempty"`
	AFFlight       string         `json:"af_flight,omitempty"`
	Route          []routeWpJSON  `json:"route,omitempty"`
	Airports       []atisAirport  `json:"airports,omitempty"` // the airports a D-ATIS request or reply is for
	PartOf         int64          `json:"part_of,omitempty"`  // this is part of (or a shorter copy of) that longer message
	Parts          []partJSON     `json:"parts,omitempty"`    // the parts and shorter copies of this message that arrived as rows of their own

	Wx        *wxJSON        `json:"wx,omitempty"`
	GS        *float64       `json:"gs,omitempty"`  // knots, as the report gave it
	Trk       *float64       `json:"trk,omitempty"` // true track, degrees
	Waypoints []waypointJSON `json:"waypoints,omitempty"`
}

// partJSON is a block or shorter copy folded under the whole message.
type partJSON struct {
	ID    int64     `json:"id"`
	T     time.Time `json:"t"`
	Label string    `json:"label"`
}

// atisAirport is where a D-ATIS message points on the map.
type atisAirport struct {
	Code string  `json:"code"`
	IATA string  `json:"iata,omitempty"`
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Type string  `json:"type,omitempty"` // arrival or departure
}

// A D-ATIS request ("D-ATIS arrival request for CYVR", or "D-ATIS request for YSSY, YMML" from Qantas) or the
// broadcast sent back ("D-ATIS arrival information P for PHNL: 060°/8 kt ...").
var atisSummary = regexp.MustCompile(`^D-ATIS (?:(arrival|departure) )?(?:request|information(?: [A-Z])?) for ([A-Z0-9]{4}(?:, [A-Z0-9]{4})*)`)

// atisAirportsOf finds the airports a D-ATIS message names (from its summary), those we know where to put.
func atisAirportsOf(m store.Message) []atisAirport {
	if m.Kind != "datis_request" && m.Kind != "weather_report" && m.Kind != "ops_report" {
		return nil
	}
	f := atisSummary.FindStringSubmatch(m.Summary)
	if f == nil {
		return nil
	}
	var out []atisAirport
	for _, code := range strings.Split(f[2], ", ") {
		if a, ok := airports.Lookup(code); ok {
			out = append(out, atisAirport{Code: a.ICAO, IATA: a.IATA, Name: a.Name, Lat: a.Lat, Lon: a.Lon, Type: f[1]})
		}
	}
	return out
}

// wxJSON is the weather at a report: wind direction (the way it blows FROM, true), speed and temperature.
type wxJSON struct {
	DirDeg *float64 `json:"dir,omitempty"`
	Kt     *float64 `json:"kt,omitempty"`
	TempC  *float64 `json:"temp_c,omitempty"`
}

// waypointJSON is a waypoint a CPDLC position report named, with the best position we can give it.
type waypointJSON struct {
	Name   string     `json:"name"`
	Role   string     `json:"role"` // passed, next or then
	Lat    float64    `json:"lat"`
	Lon    float64    `json:"lon"`
	Exact  bool       `json:"exact"`
	Source string     `json:"source"` // ADS-C, database, interpolated or projected
	How    string     `json:"how"`
	T      *time.Time `json:"t,omitempty"`
	AltFt  *float64   `json:"alt_ft,omitempty"`
}

// messages returns an aircraft's decoded datalink messages, oldest first.
// Query: hours (default 48, max 336), limit (default 300, max 1000).
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	hex := strings.ToUpper(r.PathValue("hex"))
	q := r.URL.Query()
	limit := 300
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= 1000 {
		limit = l
	}
	since, until := s.window(r, 48, 24*14)
	ms, err := s.Store.Messages(hex, since, until, limit)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	res := &waypoints.Resolver{Store: s.Store, Nav: s.Nav}
	out := make([]messageJSON, 0, len(ms))
	var segs []flights.Segment // worked out when a message needs them
	partOf, parts := foldParts(ms)
	byID := make(map[int64]store.Message, len(ms))
	for _, m := range ms {
		byID[m.ID] = m
	}
	for _, m := range ms {
		j := messageJSON{PartOf: partOf[m.ID], Undecoded: m.Kind == "unknown" || m.Summary == "MIAM (not decoded)" || strings.HasPrefix(m.Summary, "MIAM data block"), ID: m.ID, T: m.T, Label: m.Label, Kind: m.Kind, Direction: m.Direction, Summary: m.Summary,
			Detail: m.Detail, Raw: m.Text, Tail: m.Tail, Flight: m.Flight, Callsign: m.Callsign, Station: m.Station,
			RxCount: m.RxCount, Lat: m.Lat, Lon: m.Lon, AltFt: m.AltFt, PosT: m.PosT, Suspect: m.Suspect, TimeNote: m.TimeNote, TimeUnverified: m.TimeFlag,
			Mismatch: m.Mismatch, AFTail: m.AFTail, AFFlight: m.AFFlight}
		if !m.At.IsZero() && !m.At.Equal(m.T) {
			at := m.At
			j.At, j.AtBasis = &at, m.AtBasis
		}
		// A message with no flight of its own that airframes.io tagged with another flight number, and no time of its own to put it
		// there: if that flight is one we have and the message falls outside it, it belongs to that flight (it was delivered late).
		// Only for VHF, where volunteers' ground stations are known to upload late; a satellite station stamps a message as it arrives,
		// so there a different tag is the aircraft's own flight number lagging, or the next flight's data, and the time is right.
		if m.Flight == "" && m.AFFlight != "" && (m.PosT == nil || m.Lat == nil) && (m.SourceType == "vdl" || m.SourceType == "acars") {
			if segs == nil {
				segs, _ = flights.Segments(s.Store, hex, since, until)
				if segs == nil {
					segs = []flights.Segment{}
				}
			}
			at := m.T
			if !m.At.IsZero() {
				at = m.At
			}
			if o, ok := flights.Elsewhere(segs, at, m.AFFlight); ok {
				j.Elsewhere = &elsewhereJSON{Flight: m.AFFlight, ID: o.ID}
			}
		}
		j.Route = s.routeWaypoints(m.Route)
		j.Airports = atisAirportsOf(m)
		for _, id := range parts[m.ID] {
			j.Parts = append(j.Parts, partJSON{ID: id, T: byID[id].T, Label: byID[id].Label})
		}
		if m.WxDir != nil || m.WxKt != nil || m.WxTemp != nil {
			j.Wx = &wxJSON{DirDeg: m.WxDir, Kt: m.WxKt, TempC: m.WxTemp}
		}
		j.GS, j.Trk = m.GS, m.Trk
		for _, w := range res.Resolve(m) {
			wj := waypointJSON{Name: w.Name, Role: w.Role, Lat: w.Lat, Lon: w.Lon, Exact: w.Exact, Source: w.Source, How: w.How, AltFt: w.AltFt}
			if !w.T.IsZero() {
				t := w.T
				wj.T = &t
			}
			j.Waypoints = append(j.Waypoints, wj)
		}
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}

// routeWpJSON is a waypoint of an ADS-C report's predicted route, or the flight computer's own
// projection ("fixed"). The report gives coordinates only; Name is filled in when those coordinates
// are a waypoint we have learned from CPDLC reports or that is in the navigation database.
type routeWpJSON struct {
	Lat     float64  `json:"lat"`
	Lon     float64  `json:"lon"`
	AltFt   *float64 `json:"alt_ft,omitempty"`
	ETASec  *float64 `json:"eta_sec,omitempty"`
	Kind    string   `json:"kind"`
	Name    string   `json:"name,omitempty"`
	NameSrc string   `json:"name_src,omitempty"` // "learned" or "database"
}

const (
	nameTolDeg = 0.005 // about 0.3 nm: ADS-C coordinates are exact, the database's are rounded
	nameTolNM  = 0.5
)

func (s *Server) routeWaypoints(raw string) []routeWpJSON {
	if raw == "" {
		return nil
	}
	var wps []struct {
		Lat, Lon      float64
		AltFt, ETASec *float64
		Kind          string
	}
	if json.Unmarshal([]byte(raw), &wps) != nil {
		return nil
	}
	out := make([]routeWpJSON, 0, len(wps))
	for i, w := range wps {
		kind := w.Kind
		if kind == "" { // stored before kinds existed: the first two entries were next and next+1
			kind = map[int]string{0: "next", 1: "next+1"}[i]
		}
		j := routeWpJSON{Lat: w.Lat, Lon: w.Lon, AltFt: w.AltFt, ETASec: w.ETASec, Kind: kind}
		if kind != "fixed" {
			if n, ok := s.Store.FixNameAt(w.Lat, w.Lon, nameTolDeg); ok {
				j.Name, j.NameSrc = n, "learned"
			} else if n, ok := s.Nav.Near(w.Lat, w.Lon, nameTolNM); ok {
				j.Name, j.NameSrc = n, "database"
			}
		}
		out = append(out, j)
	}
	return out
}

// flightSegJSON is one flight of an aircraft, with the names of its airports where the aircraft reported them.
type flightSegJSON struct {
	flights.Segment
	FromName string `json:"from_name,omitempty"`
	ToName   string `json:"to_name,omitempty"`
}

func toFlightJSON(sg flights.Segment) flightSegJSON {
	j := flightSegJSON{Segment: sg}
	if a, ok := airports.Lookup(sg.From); ok {
		j.FromName = a.Name
	}
	if a, ok := airports.Lookup(sg.To); ok {
		j.ToName = a.Name
	}
	return j
}

// flights lists the flights an aircraft flew, oldest first. Query: hours (default 96, max 720).
func (s *Server) flights(w http.ResponseWriter, r *http.Request) {
	hex := strings.ToUpper(r.PathValue("hex"))
	hours := 96
	if h, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && h > 0 && h <= 24*30 {
		hours = h
	}
	now := s.now()
	segs, err := flights.Segments(s.Store, hex, now.Add(-time.Duration(hours)*time.Hour), now.Add(time.Minute))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]flightSegJSON, 0, len(segs))
	for _, sg := range segs {
		out = append(out, toFlightJSON(sg))
	}
	writeJSON(w, 200, out)
}

// routeAirport reads an airport typed for a flight's route: empty, or an IATA or ICAO code. A known airport is
// stored by its ICAO code, the form most messages use. A four-character code the airport list lacks is kept as
// typed (small fields are not in it); anything else unknown is refused, since it is more likely a typo.
func routeAirport(in string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(in))
	if code == "" {
		return "", nil
	}
	if a, ok := airports.Lookup(code); ok {
		if a.ICAO != "" {
			return a.ICAO, nil
		}
		return code, nil
	}
	if len(code) == 4 && icaoShape.MatchString(code) {
		return code, nil
	}
	return "", fmt.Errorf("%q is not an airport I know: use its 3-letter IATA or 4-letter ICAO code", in)
}

var icaoShape = regexp.MustCompile(`^[A-Z0-9]{4}$`)

// setFlightRoute enters the origin and destination of one flight by hand, for when no message gave them. Body:
// {"start": <unix seconds inside the flight>, "from": "KSFO", "to": "VHHH"}; either airport may be empty, and both
// empty removes the entry. Anything the messages do say about the route wins over what is entered. Answers with the flight.
func (s *Server) setFlightRoute(w http.ResponseWriter, r *http.Request) {
	hex, ok := noteHex(w, r)
	if !ok {
		return
	}
	var raw struct {
		Start int64  `json:"start"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&raw); err != nil {
		fail(w, 400, "bad JSON")
		return
	}
	from, err := routeAirport(raw.From)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	to, err := routeAirport(raw.To)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if from != "" && from == to {
		fail(w, 400, "the origin and destination are the same airport")
		return
	}
	now := s.now()
	find := func() (flights.Segment, bool, error) {
		segs, err := flights.Segments(s.Store, hex, now.Add(-720*time.Hour), now.Add(time.Minute))
		if err != nil {
			return flights.Segment{}, false, err
		}
		at := time.Unix(raw.Start, 0)
		for _, sg := range segs {
			if !at.Before(sg.Start) && !at.After(sg.End) {
				return sg, true, nil
			}
		}
		return flights.Segment{}, false, nil
	}
	sg, found, err := find()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if !found {
		fail(w, 404, "that aircraft has no flight at that time (within the last 30 days)")
		return
	}
	if err := s.Store.SetManualRoute(hex, sg.Start, sg.End, from, to, now); err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.flightMu.Lock()
	delete(s.flightCache, hex) // the airframes page should show it now, not in a minute
	s.flightMu.Unlock()
	sg, _, err = find()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, toFlightJSON(sg))
}

// window is the time range a request asks for: "hours" back from now, or, with "until" (RFC 3339), back from then, so
// a flight from last week can be fetched by itself instead of among the newest rows of a long window. until is never
// later than a minute from now.
func (s *Server) window(r *http.Request, defHours, maxHours int) (since, until time.Time) {
	hours := defHours
	if h, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && h > 0 && h <= maxHours {
		hours = h
	}
	until = s.now().Add(time.Minute)
	if u, err := time.Parse(time.RFC3339, r.URL.Query().Get("until")); err == nil && u.Before(until) {
		until = u
	}
	return until.Add(-time.Duration(hours) * time.Hour), until
}

// setFlightJoin joins the flight that started at {"start": unix seconds} to the flight before it ({"join": true}), or takes
// the joins made inside it away ({"join": false}), and answers with the flight as it now is: the joined one, starting where
// the earlier piece started. For when two pieces are one flight and the rules could not tell.
func (s *Server) setFlightJoin(w http.ResponseWriter, r *http.Request) {
	hex, ok := noteHex(w, r)
	if !ok {
		return
	}
	var raw struct {
		Start int64 `json:"start"`
		Join  bool  `json:"join"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&raw); err != nil {
		fail(w, 400, "bad JSON")
		return
	}
	now := s.now()
	at := time.Unix(raw.Start, 0)
	find := func(t time.Time) (segs []flights.Segment, i int, err error) {
		segs, err = flights.Segments(s.Store, hex, now.Add(-720*time.Hour), now.Add(time.Minute))
		for i = range segs {
			if !t.Before(segs[i].Start) && !t.After(segs[i].End) {
				return segs, i, err
			}
		}
		return segs, -1, err
	}
	segs, i, err := find(at)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if i < 0 {
		fail(w, 404, "that aircraft has no flight at that time (within the last 30 days)")
		return
	}
	sg := segs[i]
	if raw.Join {
		if i == 0 {
			fail(w, 400, "there is no earlier flight to join it to")
			return
		}
		err = s.Store.SetFlightJoin(hex, sg.Start, true, now)
	} else {
		if len(sg.Joined) == 0 {
			fail(w, 400, "that flight was not joined by hand")
			return
		}
		for _, j := range sg.Joined {
			if err = s.Store.SetFlightJoin(hex, time.Unix(j, 0), false, now); err != nil {
				break
			}
		}
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.flightMu.Lock()
	delete(s.flightCache, hex)
	s.flightMu.Unlock()
	segs, i, err = find(sg.Start)
	if err != nil || i < 0 {
		fail(w, 500, "the flight could not be found after the change")
		return
	}
	writeJSON(w, 200, toFlightJSON(segs[i]))
}
