// Package flights divides an aircraft's history into the flights it flew, so that points on the
// map and messages in the list can be shown together by flight.
//
// An aircraft does not announce "this is a new flight". What it does leave is a trail of sightings of
// the flight it is operating: the flight number in message prefixes and AFN logons, and the callsign
// on its ADS-B positions. Sightings of one flight number, with no gap over MaxGap between them,
// make a run; the same number a day later is a different run. Positions then attach to the run they
// fall in or near. Positions that no run claims, in a cluster, are a flight whose number we never
// saw: reported as an unidentified flight rather than lost.
package flights

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airlines"
	"github.com/avanha/fullflight/internal/geo"
	"github.com/avanha/fullflight/internal/store"
)

const (
	// MaxGap is the longest silence inside one flight's sightings. Longer, and the same flight
	// number is another day's flight. Satcom traffic is minutes apart, so six hours is generous.
	MaxGap = 6 * time.Hour
	// AttachSlack is how far outside a run's sightings a position may be and still belong to it.
	AttachSlack = 60 * time.Minute
	// ClusterGap separates unidentified flights: positions further apart than this are different flights.
	ClusterGap = 90 * time.Minute
	// MinUnidentified is how many positions a cluster needs to count as a flight at all.
	MinUnidentified = 2
	// A run of at most StrayEvidence sightings spanning under StrayWithin, between two runs of one
	// flight number, is a stray (one mislabelled message), not a flight of its own.
	StrayEvidence = 2
	StrayWithin   = 10 * time.Minute
	// RouteSlack widens a flight's window when looking for its REP081 origin/destination.
	RouteSlack = 30 * time.Minute
	// Pieces of one flight can be far apart when the aircraft was quiet or its number was only
	// logged late. Two neighbouring pieces that report the same origin and destination are one flight
	// if no more than MaxRouteGap separates them, and it ends up no longer than MaxFlight.
	MaxRouteGap = 14 * time.Hour
	MaxFlight   = 24 * time.Hour
	// An unnumbered piece right next to a numbered flight that has no route to compare is that
	// flight's missing start or end, if it is within AdoptWithin.
	AdoptWithin = 2 * time.Hour
	// A stop on the ground at least this long, seen by ADS-B, ends a flight even if the next one has the same number:
	// the aircraft flew one leg, stopped, and flew the next (UPS65 from Hong Kong to Anchorage, then on to Louisville).
	StopoverMin = 45 * time.Minute
	// A departure up to DepartureSlack before a flight's first sighting is that flight's: the aircraft may be
	// first heard some time after it left.
	DepartureSlack = 2 * time.Hour
	// A sighting from an airline's operations message (store.Evidence.Weak) that falls between two sightings of one other
	// flight by the aircraft itself, each within WeakWithin, is about the next flight, not this one, and is set aside.
	WeakWithin = 2 * time.Hour
	// Over a long silence, two pieces of one flight number are one flight if the aircraft was airborne at the end of the
	// first and at the start of the second and the speed it would have needed between them is that of a flight, not of a
	// turnaround or a stop: a day later the same number is a flight from the other end of the route, which would take the
	// speed far above BridgeMaxKt, or from the same place, far below BridgeMinKt.
	BridgeMaxGap    = 20 * time.Hour
	BridgeMaxFlight = 26 * time.Hour
	BridgeMinKt     = 300.0
	BridgeMaxKt     = 650.0
	BridgeMinAltFt  = 10000.0
	// ...and it must match the speed the aircraft was measured flying just before the silence or just after it, to within this.
	BridgeSpeedTolerance = 0.3
	manualJoinSlack      = 90 * time.Second // the start a join was made at, give or take
	// An aircraft that has sat for StaleGap sends its last callsign again when it powers up, then changes it to the next
	// flight's within StaleWithin. Those first sightings, under the old number, are the next flight's and not a late end to
	// the last one, when that next flight is then heard from at least StaleFollowers times in the following ten minutes.
	StaleGap       = time.Hour
	StaleWithin    = 2 * time.Minute
	StaleFollowers = 5
)

type Segment struct {
	ID          string    `json:"id"`
	Flight      string    `json:"flight,omitempty"`   // IATA form where we know the airline, e.g. CX113
	Callsign    string    `json:"callsign,omitempty"` // ICAO form, e.g. CPA113
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Messages    int       `json:"messages"`
	Positions   int       `json:"positions"`
	From        string    `json:"from,omitempty"` // from REP081, where the aircraft sends one
	To          string    `json:"to,omitempty"`
	FromManual  bool      `json:"from_manual,omitempty"` // From was entered by hand, because no message gave one
	ToManual    bool      `json:"to_manual,omitempty"`
	Departed    time.Time `json:"departed,omitzero"`      // when it left, if we know
	DepartedHow string    `json:"departed_how,omitempty"` // "takeoff", "adsb", "pushback" or "taxi": see store.Departure
	Identified  bool      `json:"identified"`             // false: a flight whose number we never saw
	Latest      bool      `json:"latest"`                 // the most recent flight

	Joined []int64 `json:"joined,omitempty"` // when the pieces joined to this flight by hand started (unix seconds)

	stopAfter bool // the aircraft stopped on the ground after this flight, and the next piece is another leg: never joined
}

type ident struct {
	airline, num string // airline is the IATA code where known
	icao, iata   string
	display      string
}

var (
	icaoRe = regexp.MustCompile(`^([A-Z]{3})0*(\d{1,4}[A-Z]?)$`)
	// an IATA airline code is two characters with at least one letter (B6, 6E, CX)
	iataRe = regexp.MustCompile(`^([A-Z][A-Z0-9]|[0-9][A-Z])0*(\d{1,4}[A-Z]?)$`)
)

// parse reads a flight identifier in either form. "CX0", an all-zero number, is garbage.
func parse(id string) (ident, bool) {
	id = strings.ToUpper(strings.TrimSpace(id))
	m := icaoRe.FindStringSubmatch(id)
	if m == nil {
		m = iataRe.FindStringSubmatch(id)
	}
	if m == nil || strings.Trim(m[2], "0") == "" {
		return ident{}, false
	}
	m[1] = airlines.Canonical(m[1]) // UPS writes UP65 for the flight whose callsign is UPS65
	x := ident{airline: m[1], num: m[2]}
	x.icao, x.iata = airlines.Forms(m[1] + m[2])
	x.display = m[1] + m[2]
	if x.iata != "" {
		x.airline = x.iata[:len(x.iata)-len(m[2])]
		x.display = x.iata
	}
	return x, true
}

// same reports whether two identifiers are one flight: the same number and airline, the airline
// written either way. If we have no table entry to tell us "CX" is "CPA", a 2- and 3-letter code
// starting with the same letter still match.
func same(a, b ident) bool {
	if a.num != b.num {
		return false
	}
	if a.airline == b.airline {
		return true
	}
	if len(a.airline) == 2 && len(b.airline) == 3 {
		return b.airline[0] == a.airline[0]
	}
	if len(a.airline) == 3 && len(b.airline) == 2 {
		return a.airline[0] == b.airline[0]
	}
	return false
}

type run struct {
	id          ident
	first, last time.Time
	n           int
	forms       map[string]int // how often each display form was seen
	positions   int
}

func (r *run) add(e ident, t time.Time) {
	r.last = t
	r.n++
	r.forms[e.display]++
}

func (r *run) best() string {
	best, n := "", 0
	for f, c := range r.forms {
		if c > n || (c == n && f < best) {
			best, n = f, c
		}
	}
	return best
}

// distance is how far t is outside [first, last]: zero inside.
func (r *run) distance(t time.Time) time.Duration {
	switch {
	case t.Before(r.first):
		return r.first.Sub(t)
	case t.After(r.last):
		return t.Sub(r.last)
	}
	return 0
}

// Segments returns the aircraft's flights in [since, until], oldest first.
func Segments(st *store.Store, hexAddr string, since, until time.Time) ([]Segment, error) {
	ev, err := st.FlightEvidence(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	posTimes, err := st.PositionTimes(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	ev = setAsideWeak(ev)
	ev = setAsideStale(ev)

	// 1. runs of one flight number
	var runs []*run
	for _, e := range ev {
		id, ok := parse(e.ID)
		if !ok {
			continue
		}
		if n := len(runs); n > 0 && same(runs[n-1].id, id) && e.T.Sub(runs[n-1].last) <= MaxGap {
			runs[n-1].add(id, e.T)
			continue
		}
		r := &run{id: id, first: e.T, last: e.T, forms: map[string]int{}}
		r.add(id, e.T)
		r.n = 1
		runs = append(runs, r)
	}

	// 2. a stray sighting of another number inside a flight does not split it
	for again := true; again; {
		again = false
		for i := 1; i+1 < len(runs); i++ {
			r := runs[i]
			if r.n <= StrayEvidence && r.last.Sub(r.first) <= StrayWithin && same(runs[i-1].id, runs[i+1].id) &&
				runs[i+1].first.Sub(runs[i-1].last) <= MaxGap {
				a, b := runs[i-1], runs[i+1]
				a.last, a.n = b.last, a.n+b.n
				for f, c := range b.forms {
					a.forms[f] += c
				}
				runs = append(runs[:i], runs[i+2:]...)
				again = true
				break
			}
		}
	}

	// 2b. over a long silence, the same flight number is one flight if the aircraft was in the air at both ends of it
	fixes, err := st.AirFixes(hexAddr, since.Add(-BridgeMaxGap), until.Add(BridgeMaxGap))
	if err != nil {
		return nil, err
	}
	runs = bridgeSilences(runs, fixes)

	// 3. positions go to the run they are in or nearest to (within AttachSlack)
	var orphans []time.Time
	for _, t := range posTimes {
		var best *run
		var bestD time.Duration
		for _, r := range runs {
			if d := r.distance(t); d <= AttachSlack && (best == nil || d < bestD) {
				best, bestD = r, d
			}
		}
		if best == nil {
			orphans = append(orphans, t)
			continue
		}
		best.positions++
		if t.Before(best.first) {
			best.first = t
		}
		if t.After(best.last) {
			best.last = t
		}
	}

	// 4. a run with no position is not a flight we can show (ground chatter, a stray number)
	var segs []Segment
	for _, r := range runs {
		if r.positions == 0 {
			continue
		}
		s := Segment{Start: r.first, End: r.last, Positions: r.positions, Identified: true}
		s.Flight = r.id.iata
		s.Callsign = r.id.icao
		if s.Flight == "" {
			s.Flight = r.best()
		}
		segs = append(segs, s)
	}

	// 5. positions no run claims, together, are a flight whose number we never saw
	for i := 0; i < len(orphans); {
		j := i + 1
		for j < len(orphans) && orphans[j].Sub(orphans[j-1]) <= ClusterGap {
			j++
		}
		if j-i >= MinUnidentified {
			segs = append(segs, Segment{Start: orphans[i], End: orphans[j-1], Positions: j - i})
		}
		i = j
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start.Before(segs[j].Start) })

	// 5b. a long stop on the ground in the middle of a flight makes it two
	stops, err := st.GroundStops(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	segs = splitAtStopovers(segs, stops, ev, posTimes)

	// 6. the route the aircraft reported; pieces of one flight that share it are joined
	routes, err := st.RouteReports(hexAddr, since.Add(-RouteSlack), until.Add(RouteSlack))
	if err != nil {
		return nil, err
	}
	// A report belongs to the flight it happened during. One that reached us long after (an A350 keeps its reports and delivers them
	// later) counts for the flight it arrived in only if no flight of ours holds the moment it happened.
	holds := func(s Segment, t time.Time) bool {
		return !t.Before(s.Start.Add(-RouteSlack)) && !t.After(s.End.Add(RouteSlack))
	}
	setRoute := func(s *Segment) {
		var mine []store.RouteReport
		for _, r := range routes {
			happened := false
			for _, o := range segs {
				if holds(o, r.T) {
					happened = true
					break
				}
			}
			if holds(*s, r.T) || (!happened && holds(*s, r.Rx)) {
				mine = append(mine, r)
			}
		}
		s.From, s.To = modalRoute(mine, time.Time{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	}
	for i := range segs {
		setRoute(&segs[i])
	}
	for merged := true; merged; {
		merged = false
		for i := 0; i+1 < len(segs); i++ {
			if a, b := segs[i], segs[i+1]; joinable(a, b) {
				segs[i] = join(a, b)
				setRoute(&segs[i])
				segs = append(segs[:i+1], segs[i+2:]...)
				merged = true
				break
			}
		}
	}
	// 7. an origin or destination typed in by hand fills in one the messages never gave (what they give wins)
	manual, err := st.ManualRoutes(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	for i := range segs {
		applyManualRoute(&segs[i], manual)
	}
	// 8. when each flight left, from the best evidence there is
	deps, err := st.Departures(hexAddr, since.Add(-DepartureSlack), until)
	if err != nil {
		return nil, err
	}
	for i := range segs {
		segs[i].Departed, segs[i].DepartedHow = departureOf(segs[i], deps)
	}
	// 9. flights joined by hand
	joins, err := st.FlightJoins(hexAddr, since.Add(-MaxFlight), until)
	if err != nil {
		return nil, err
	}
	segs = applyJoins(segs, joins)
	for i := range segs {
		segs[i].Messages = st.CountMessages(hexAddr, segs[i].Start, segs[i].End)
		segs[i].ID = segID(segs[i])
	}
	if n := len(segs); n > 0 {
		segs[n-1].Latest = true
	}
	return segs, nil
}

// splitAtStopovers cuts a flight in two where ADS-B saw the aircraft sit on the ground for StopoverMin or more, between
// its landing and its next departure. The cut is the middle of the stop: what was sighted before it ends the first
// leg and what was sighted after it starts the second, so the paperwork for the next leg (its clearance, its schedule)
// belongs to that leg and the arrival's chatter to the one before.
func splitAtStopovers(segs []Segment, stops []store.GroundStop, ev []store.Evidence, posTimes []time.Time) []Segment {
	for _, stop := range stops {
		if stop.Depart.Sub(stop.Land) < StopoverMin {
			continue
		}
		for i, sg := range segs {
			if stop.Land.Before(sg.Start) || stop.Depart.After(sg.End) {
				continue
			}
			cut := stop.Land.Add(stop.Depart.Sub(stop.Land) / 2)
			var lastBefore, firstAfter time.Time
			consider := func(t time.Time) {
				if t.Before(sg.Start) || t.After(sg.End) {
					return
				}
				if !t.After(cut) && t.After(lastBefore) {
					lastBefore = t
				}
				if t.After(cut) && (firstAfter.IsZero() || t.Before(firstAfter)) {
					firstAfter = t
				}
			}
			for _, e := range ev {
				consider(e.T)
			}
			for _, t := range posTimes {
				consider(t)
			}
			if lastBefore.IsZero() || firstAfter.IsZero() {
				break
			}
			count := func(from, to time.Time) (n int) {
				for _, t := range posTimes {
					if !t.Before(from) && !t.After(to) {
						n++
					}
				}
				return
			}
			a, b := sg, sg
			a.End, a.stopAfter = lastBefore, true
			b.Start = firstAfter
			a.Positions, b.Positions = count(a.Start, a.End), count(b.Start, b.End)
			segs = append(segs[:i], append([]Segment{a, b}, segs[i+1:]...)...)
			break
		}
	}
	return segs
}

// applyManualRoute fills in the origin and destination of sg that the messages left empty, from the hand-entered
// route covering it. Where several cover it (a flight that grew to take in two), the newest wins.
func applyManualRoute(sg *Segment, manual []store.ManualRoute) {
	var best *store.ManualRoute
	for i := range manual {
		m := &manual[i]
		if m.Start.After(sg.End) || m.End.Before(sg.Start) {
			continue
		}
		if best == nil || m.Created.After(best.Created) || (m.Created.Equal(best.Created) && m.ID > best.ID) {
			best = m
		}
	}
	if best == nil {
		return
	}
	if sg.From == "" && best.From != "" {
		sg.From, sg.FromManual = best.From, true
	}
	if sg.To == "" && best.To != "" {
		sg.To, sg.ToManual = best.To, true
	}
}

// departureOf picks the departure that belongs to a flight: the best kind of evidence in its span (a takeoff report
// before ADS-B seeing it leave the ground before a gate time), the earliest of that kind.
func departureOf(sg Segment, deps []store.Departure) (time.Time, string) {
	rank := map[string]int{"takeoff": 0, "adsb": 1, "pushback": 2, "taxi": 3}
	var best store.Departure
	bestRank := len(rank)
	for _, d := range deps {
		if d.T.Before(sg.Start.Add(-DepartureSlack)) || d.T.After(sg.End) {
			continue
		}
		if r, ok := rank[d.How]; ok && r < bestRank {
			best, bestRank = d, r
		}
	}
	if bestRank == len(rank) {
		return time.Time{}, ""
	}
	return best.T, best.How
}

// modalRoute is the origin/destination reported most often in [from, to], the latest on a tie.
func modalRoute(rs []store.RouteReport, from, to time.Time) (string, string) {
	type pair struct{ from, to string }
	count := map[pair]int{}
	last := map[pair]time.Time{}
	for _, r := range rs {
		if r.T.Before(from) || r.T.After(to) {
			continue
		}
		p := pair{r.From, r.To}
		count[p]++
		last[p] = r.T
	}
	var best pair
	n := 0
	for p, c := range count {
		if c > n || (c == n && last[p].After(last[best])) {
			best, n = p, c
		}
	}
	return best.from, best.to
}

func segID(s Segment) string {
	name := s.Flight
	if !s.Identified {
		name = "?"
	}
	return fmt.Sprintf("%s@%d", name, s.Start.Unix())
}

// joinable reports whether b, the next flight after a, is really the same flight as a.
func joinable(a, b Segment) bool {
	if a.stopAfter {
		return false // the aircraft stopped between them: two legs
	}
	if a.Identified && b.Identified && !sameFlightNumber(a, b) {
		return false // two different numbered flights are never one
	}
	if b.End.Sub(a.Start) > MaxFlight {
		return false
	}
	gap := b.Start.Sub(a.End)
	if a.From != "" && a.To != "" && a.From == b.From && a.To == b.To {
		return gap <= MaxRouteGap // the same route, nothing between them
	}
	// one piece has a route and the other none: an unnumbered piece next to a numbered flight
	// that has no route of its own is taken to be part of it, if close
	if a.Identified != b.Identified && gap <= AdoptWithin && (a.From == "" || b.From == "") {
		return true
	}
	return false
}

func sameFlightNumber(a, b Segment) bool {
	x, ok1 := parse(a.Flight)
	y, ok2 := parse(b.Flight)
	return ok1 && ok2 && same(x, y)
}

// join combines two pieces of one flight, keeping the identity of whichever has one.
func join(a, b Segment) Segment {
	out := a
	if !a.Identified && b.Identified {
		out = b
	}
	out.Start = minTime(a.Start, b.Start)
	out.End = maxTime(a.End, b.End)
	out.Positions = a.Positions + b.Positions
	out.stopAfter = b.stopAfter
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// setAsideStale drops the first sightings of an aircraft that has come back to life after a long silence under the number it
// last flew, when within StaleWithin it changes to another and carries on under that. The positions it sent in those seconds
// belong to the new flight, which the nearest-run rule gives them to; left in, they would stretch the old flight over the whole
// time on the ground and into the next flight's climb.
func setAsideStale(ev []store.Evidence) []store.Evidence {
	var drop map[int]bool
	for i := 1; i < len(ev); i++ {
		if ev[i].T.Sub(ev[i-1].T) <= StaleGap {
			continue
		}
		old, ok := parse(ev[i].ID)
		if prev, pok := parse(ev[i-1].ID); !ok || !pok || !same(old, prev) {
			continue
		}
		k := i
		for k < len(ev) && ev[k].T.Sub(ev[i].T) <= StaleWithin {
			if id, ok := parse(ev[k].ID); !ok || !same(old, id) {
				break
			}
			k++
		}
		if k >= len(ev) || ev[k].T.Sub(ev[k-1].T) > StaleWithin {
			continue
		}
		next, ok := parse(ev[k].ID)
		if !ok || same(old, next) {
			continue
		}
		followers := 0
		for j := k; j < len(ev) && ev[j].T.Sub(ev[k].T) <= 10*time.Minute; j++ {
			if id, ok := parse(ev[j].ID); ok && same(next, id) {
				followers++
			}
		}
		if followers < StaleFollowers {
			continue
		}
		if drop == nil {
			drop = map[int]bool{}
		}
		for j := i; j < k; j++ {
			drop[j] = true
		}
	}
	if drop == nil {
		return ev
	}
	out := make([]store.Evidence, 0, len(ev))
	for i, e := range ev {
		if !drop[i] {
			out = append(out, e)
		}
	}
	return out
}

// setAsideWeak drops the weak sightings that are inside another flight: the aircraft itself named a different flight within
// WeakWithin before and again within WeakWithin after. A cockpit asking for the weather along the next flight's route, or the
// airline sending that flight's data, while the aircraft is still flying this one, does not start the next flight; it starts
// when the aircraft itself says so. A weak sighting at either end of a flight, after the last thing the aircraft said or before
// the first, is left: that is the flight just finished or the one about to start.
func setAsideWeak(ev []store.Evidence) []store.Evidence {
	var strong []store.Evidence
	for _, e := range ev {
		if !e.Weak {
			strong = append(strong, e)
		}
	}
	if len(strong) == len(ev) {
		return ev
	}
	out := make([]store.Evidence, 0, len(ev))
	for _, e := range ev {
		if e.Weak && insideAnother(e, strong) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// insideAnother reports whether the weak sighting w falls inside a flight the aircraft itself named otherwise: a strong
// sighting of one flight just before it and another of the same flight just after.
func insideAnother(w store.Evidence, strong []store.Evidence) bool {
	id, ok := parse(w.ID)
	if !ok {
		return false
	}
	i := sort.Search(len(strong), func(i int) bool { return !strong[i].T.Before(w.T) }) // the first at or after w
	for k := i - 1; k >= 0 && w.T.Sub(strong[k].T) <= WeakWithin; k-- {
		before, bok := parse(strong[k].ID)
		if !bok || same(id, before) {
			continue
		}
		for j := i; j < len(strong) && strong[j].T.Sub(w.T) <= WeakWithin; j++ {
			if after, aok := parse(strong[j].ID); aok && same(before, after) {
				return true
			}
		}
		return false // the nearest strong sighting before is another flight, and that flight is not heard from afterwards
	}
	return false
}

// bridgeSilences joins neighbouring runs of one flight number that are further apart than MaxGap, when the fixes either
// side of the silence show the aircraft airborne and the speed it would have flown across it is the speed it was flying:
// the average over the silence is within BridgeSpeedTolerance of the speed measured between the last fixes before it, or the
// first after it. (A stop on the ground in the middle averages far slower than the aircraft flew.)
func bridgeSilences(runs []*run, fixes []store.AirFix) []*run {
	airborne := func(f store.AirFix) bool { return !f.OnGround && f.AltFt != nil && *f.AltFt >= BridgeMinAltFt }
	// the speed between fix k and the nearest fix at least two minutes before it (delta -1) or after it (delta 1), if there
	// is one within half an hour: the speed the aircraft was flying there
	speedAt := func(k, delta int) float64 {
		for j := k + delta; j >= 0 && j < len(fixes); j += delta {
			dt := fixes[k].T.Sub(fixes[j].T)
			if dt < 0 {
				dt = -dt
			}
			if dt < 2*time.Minute {
				continue
			}
			if dt > 30*time.Minute {
				return 0
			}
			return geo.DistNM(fixes[k].Lat, fixes[k].Lon, fixes[j].Lat, fixes[j].Lon) / dt.Hours()
		}
		return 0
	}
	for again := true; again; {
		again = false
		for i := 0; i+1 < len(runs); i++ {
			a, b := runs[i], runs[i+1]
			gap := b.first.Sub(a.last)
			if !same(a.id, b.id) || gap <= MaxGap || gap > BridgeMaxGap || b.last.Sub(a.first) > BridgeMaxFlight {
				continue
			}
			// the fixes from the last that belongs to a to the first that belongs to b, and the longest silence between two of them:
			// that is the stretch to account for
			k1, k2 := -1, -1
			for k := range fixes {
				f := fixes[k]
				if !f.T.After(a.last.Add(AttachSlack)) && !f.T.Before(a.first.Add(-AttachSlack)) {
					k1 = k
				}
				if k2 < 0 && !f.T.Before(b.first.Add(-AttachSlack)) && !f.T.After(b.last.Add(AttachSlack)) {
					k2 = k
				}
			}
			if k1 < 0 || k2 <= k1 {
				continue
			}
			p := k1
			for k := k1; k < k2; k++ {
				if fixes[k+1].T.Sub(fixes[k].T) > fixes[p+1].T.Sub(fixes[p].T) {
					p = k
				}
			}
			f1, f2 := fixes[p], fixes[p+1]
			if !f2.T.After(f1.T) || !airborne(f1) || !airborne(f2) {
				continue
			}
			kt := geo.DistNM(f1.Lat, f1.Lon, f2.Lat, f2.Lon) / f2.T.Sub(f1.T).Hours()
			ok := false
			for _, ref := range []float64{speedAt(p, -1), speedAt(p+1, 1)} {
				if ref > 0 && kt >= ref*(1-BridgeSpeedTolerance) && kt <= ref*(1+BridgeSpeedTolerance) {
					ok = true
				}
			}
			if !ok || kt < BridgeMinKt || kt > BridgeMaxKt {
				continue
			}
			a.last, a.n = b.last, a.n+b.n
			for f, c := range b.forms {
				a.forms[f] += c
			}
			runs = append(runs[:i+1], runs[i+2:]...)
			again = true
			break
		}
	}
	return runs
}

// applyJoins merges each flight that was joined by hand with the flight before it. A join is remembered by when the later
// piece started; it applies to the flight that covers that moment.
func applyJoins(segs []Segment, joins []time.Time) []Segment {
	for k := len(joins) - 1; k >= 0; k-- { // latest first, so earlier positions stay where they were
		at := joins[k]
		for i := 1; i < len(segs); i++ {
			if at.Before(segs[i].Start.Add(-manualJoinSlack)) || at.After(segs[i].End) {
				continue
			}
			a, b := segs[i-1], segs[i]
			m := a
			m.End = b.End
			m.Positions += b.Positions
			if m.From == "" {
				m.From, m.FromManual = b.From, b.FromManual
			}
			if b.To != "" {
				m.To, m.ToManual = b.To, b.ToManual
			}
			if !m.Identified && b.Identified {
				m.Identified, m.Flight, m.Callsign = true, b.Flight, b.Callsign
			}
			m.Latest = false
			m.stopAfter = b.stopAfter
			m.Joined = append(append([]int64{}, a.Joined...), b.Start.Unix())
			m.Joined = append(m.Joined, b.Joined...)
			segs = append(segs[:i-1], append([]Segment{m}, segs[i+1:]...)...)
			break
		}
	}
	return segs
}
