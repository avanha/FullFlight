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
	"github.com/avanha/fullflight/internal/airports"
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
	// Stops ADS-B never saw: a fix this long after the one before, climbing out of an airport far from where the aircraft was, shows
	// it landed and took off again between them (QF1 from Sydney to Singapore and on to London, with ADS-B only for the second).
	// It had time to get there at UnseenCruiseKt, with StopoverMin to spare.
	UnseenStopGap  = 3 * time.Hour
	UnseenCruiseKt = 450.0
	unseenNearNM   = 8.0  // how close to an airport the climb-out fix is
	unseenFromNM   = 40.0 // how far the fix before it was from that airport
	unseenClimbFt  = 6000.0
	// ...or the fix before it was an approach to that airport (low, close and descending) and the silence after it lasted this long:
	// it landed, and nothing it did in the next hour would be circling (QF1 at Singapore: down to 1,600 ft six miles out, then nothing
	// for two hours and thirty-nine minutes, then the climb-out).
	unseenApproachGap = time.Hour
	unseenApproachNM  = 8.0
	unseenApproachFt  = 2000.0
	onFinalFt         = 3000.0 // a single fix this low and this close (onFinalNM) is an approach, with no earlier fix to show it descending
	onFinalNM         = 3.0
	approachKt        = 150.0 // for the time left to touchdown from the last fix of an approach
	departedNM        = 4.0   // how close to an airport the fix on the ground is, for a flight seen taking off from it
	// A flight no sighting named is shown under the number airframes.io tagged its messages with, if at least TagMin sound ones all agree.
	TagMin         = 2
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
	RouteSource string    `json:"route_source,omitempty"` // what kind of report gave From and To (REP081, A350 report, airline report, ACMF, flight plan)
	RouteAt     time.Time `json:"route_at,omitzero"`      // when the newest report that agrees with them was made
	SeenBy      string    `json:"seen_by,omitempty"`      // what carried the last sighting of the flight's number: "ADS-B" or "messages"
	SeenAt      time.Time `json:"seen_at,omitzero"`       // and when it was made
	FromManual  bool      `json:"from_manual,omitempty"`  // From was entered by hand, because no message gave one
	FromSeen    bool      `json:"from_seen,omitempty"`    // From is where ADS-B saw the flight climb out, because no message gave one
	ToSeen      bool      `json:"to_seen,omitempty"`      // To is where ADS-B saw the flight land, or where the next leg started, because no message gave one
	ToManual    bool      `json:"to_manual,omitempty"`
	Departed    time.Time `json:"departed,omitzero"`      // when it left, if we know
	DepartedHow string    `json:"departed_how,omitempty"` // "takeoff", "adsb", "pushback" or "taxi": see store.Departure
	Identified  bool      `json:"identified"`             // false: a flight whose number we never saw
	Latest      bool      `json:"latest"`                 // the most recent flight

	Joined []int64 `json:"joined,omitempty"` // when the pieces joined to this flight by hand started (unix seconds)

	stopAfter bool // the aircraft stopped on the ground after this flight, and the next piece is another leg: never joined

	FlightFromAF bool `json:"flight_from_af,omitempty"` // Flight is airframes.io's tag on the messages, not a sighting: the flight is still unidentified

	Pending bool `json:"pending,omitempty"` // named by messages, with no position yet: the next flight. Only when asked for (see segments)

	Why []Note `json:"why,omitempty"` // how it came about: only when asked for (Explain)
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

// Lookback is how far before a window the flights are worked out, so that a flight which began before it is whole (its start,
// and so its identity, do not depend on where the window happens to begin) and so that a report delivered late finds the earlier
// flight it belongs to. An A350 delivers its stored reports up to a few days late.
const Lookback = 7 * 24 * time.Hour

// Current is Segments with the flight still to leave: if the newest flight the aircraft's messages name has no position yet (the
// next flight, announced while the aircraft is at the gate), it comes last, marked Pending. For showing what the aircraft is doing
// now; the flights to draw and list are Segments.
func Current(st *store.Store, hexAddr string, since, until time.Time) ([]Segment, error) {
	segs, err := segments(st, hexAddr, since.Add(-Lookback), until, nil, true)
	if err != nil {
		return nil, err
	}
	out := segs[:0]
	for _, sg := range segs {
		if !sg.End.Before(since) {
			out = append(out, sg)
		}
	}
	return out, nil
}

// Segments returns the aircraft's flights in [since, until], oldest first: those that end in the window or were still going
// at its start.
func Segments(st *store.Store, hexAddr string, since, until time.Time) ([]Segment, error) {
	segs, err := segments(st, hexAddr, since.Add(-Lookback), until, nil, false)
	if err != nil {
		return nil, err
	}
	out := segs[:0]
	for _, sg := range segs {
		if !sg.End.Before(since) {
			out = append(out, sg)
		}
	}
	return out, nil
}

// segments does the work of Segments. With pending, the newest flight the aircraft's messages name that has no position yet (the
// next flight, announced while the aircraft is at the gate) is returned too, marked Pending.
func segments(st *store.Store, hexAddr string, since, until time.Time, tr *trace, pending bool) ([]Segment, error) {
	ev, err := st.FlightEvidence(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	posTimes, err := st.PositionTimes(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	ev = setAsideWeak(ev, tr)
	ev = setAsideStale(ev, tr)

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
				tr.note("runs", "stray", r.first, r.last, "%d sighting(s) of %s between two runs of %s are a stray, not a flight", r.n, r.id.display, a.id.display)
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
	runs = bridgeSilences(runs, fixes, tr)

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
	var waiting *run // the newest run with no position, which may be the next flight
	for _, r := range runs {
		if r.positions == 0 {
			waiting = r
			tr.note("runs", "dropped", r.first, r.last, "%d sighting(s) of %s and no position: ground chatter or a stray number, not a flight we can show", r.n, r.id.display)
			continue
		}
		tr.note("runs", "run", r.first, r.last, "%d sighting(s) of %s (written %s) and %d position(s) attached", r.n, r.id.display, formsText(r.forms), r.positions)
		s := Segment{Start: r.first, End: r.last, Positions: r.positions, Identified: true}
		s.Flight = r.id.iata
		s.Callsign = r.id.icao
		if s.Flight == "" {
			s.Flight = r.best()
		}
		segs = append(segs, s)
	}

	if waiting != nil && pending && len(segs) > 0 && !waiting.last.After(segs[len(segs)-1].End) {
		waiting = nil // not newer than a flight we have: ground chatter of that one
	}
	if waiting != nil && pending {
		s := Segment{Start: waiting.first, End: waiting.last, Identified: true, Pending: true, Flight: waiting.id.iata, Callsign: waiting.id.icao}
		if s.Flight == "" {
			s.Flight = waiting.best()
		}
		tr.note("runs", "pending", s.Start, s.End, "%s: sighted %d time(s) and no position yet: the next flight, still to leave", s.Flight, waiting.n)
		segs = append(segs, s)
	}

	// 5. positions no run claims, together, are a flight whose number we never saw
	for i := 0; i < len(orphans); {
		j := i + 1
		for j < len(orphans) && orphans[j].Sub(orphans[j-1]) <= ClusterGap {
			j++
		}
		if j-i >= MinUnidentified {
			tr.note("positions", "unidentified", orphans[i], orphans[j-1], "%d position(s) no run claims, within %v of one another: a flight whose number we never saw", j-i, ClusterGap)
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
	unseen := unseenStops(fixes)
	for _, u := range unseen {
		stops = append(stops, u.GroundStop)
		tr.note("stops", "unseen-stop", u.Land, u.Depart, "a stop at %s that no fix showed: the fix at %s is low and next to it, on the ground or climbing, long after one far away or one on the way in", u.Airport, u.Depart.Format("15:04:05"))
	}
	segs = splitAtStopovers(segs, stops, ev, posTimes, tr)
	// A stop that falls between two pieces, not inside one, divides them as surely: they are never one flight.
	for _, u := range unseen {
		for i := 0; i+1 < len(segs); i++ {
			if !segs[i].End.After(u.Land) && !segs[i+1].Start.Before(u.Land) && u.Depart.Sub(u.Land) >= StopoverMin {
				segs[i].stopAfter = true
			}
		}
	}

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
		c := modalRoute(mine, time.Time{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
		s.From, s.To, s.RouteSource, s.RouteAt = c.From, c.To, c.Src, c.At
		tr.note("route", "route", s.Start, s.End, "%d route report(s) belong to it; %s > %s is the most reported, by %d", len(mine), s.From, s.To, c.N)
	}
	for i := range segs {
		setRoute(&segs[i])
	}
	for merged := true; merged; {
		merged = false
		for i := 0; i+1 < len(segs); i++ {
			if a, b := segs[i], segs[i+1]; joinable(a, b) {
				tr.note("route", "join", a.Start, b.End, "%s: two pieces joined, %s", firstNonEmpty(a.Flight, b.Flight, "unnumbered"), joinReason(a, b))
				segs[i] = join(a, b)
				setRoute(&segs[i])
				segs = append(segs[:i+1], segs[i+2:]...)
				merged = true
				break
			}
		}
	}
	if tr != nil {
		for i := 0; i+1 < len(segs); i++ {
			if a, b := segs[i], segs[i+1]; a.Identified && b.Identified && sameFlightNumber(a, b) {
				tr.note("route", "not-joined", a.End, b.Start, "%s: two pieces of one number kept apart, %s", a.Flight, notJoinedReason(a, b))
			}
		}
	}
	// A flight that ADS-B saw climb out of an airport after a stop it never saw, and whose messages gave no origin, left from there.
	for _, u := range unseen {
		for i := range segs {
			if sg := &segs[i]; sg.From == "" && sg.Start.After(u.Land) && !sg.Start.After(u.Depart) && !sg.End.Before(u.Depart) {
				sg.From, sg.FromSeen = u.Airport, true
				tr.note("route", "seen-origin", sg.Start, sg.End, "origin %s taken from the climb-out ADS-B saw after a stop it never saw", u.Airport)
			}
		}
	}
	// So did one whose first fix was on the ground at an airport and that was then seen in the air: ADS-B watched it take off.
	for i := range segs {
		if sg := &segs[i]; sg.From == "" {
			if a, ok := leftFrom(fixes, sg.Start, sg.End); ok {
				sg.From, sg.FromSeen = a, true
				tr.note("route", "seen-origin", sg.Start, sg.End, "origin %s taken from ADS-B seeing it on the ground there and then in the air", a)
			}
		}
	}
	// A flight no message gave a destination for went where ADS-B saw it land, or failing that where the next leg began after a stop.
	for i := range segs {
		sg := &segs[i]
		if sg.To != "" {
			continue
		}
		if a, ok := landedAt(fixes, sg.Start, sg.End); ok {
			sg.To, sg.ToSeen = a, true
			tr.note("route", "seen-destination", sg.Start, sg.End, "destination %s taken from ADS-B seeing it land there", a)
		} else if sg.stopAfter && i+1 < len(segs) && segs[i+1].From != "" {
			sg.To, sg.ToSeen = segs[i+1].From, true
			tr.note("route", "seen-destination", sg.Start, sg.End, "destination %s taken from where the next leg began after a stop", sg.To)
		}
	}
	// 7. an origin or destination typed in by hand fills in one the messages never gave (what they give wins)
	manual, err := st.ManualRoutes(hexAddr, since, until)
	if err != nil {
		return nil, err
	}
	for i := range segs {
		from, to := segs[i].From, segs[i].To
		applyManualRoute(&segs[i], manual)
		if segs[i].From != from || segs[i].To != to {
			tr.note("manual", "manual-route", segs[i].Start, segs[i].End, "route entered by hand fills in %s > %s (messages gave %q > %q)", segs[i].From, segs[i].To, from, to)
		}
	}
	// 8. when each flight left, from the best evidence there is
	deps, err := st.Departures(hexAddr, since.Add(-DepartureSlack), until)
	if err != nil {
		return nil, err
	}
	for i := range segs {
		segs[i].Departed, segs[i].DepartedHow = departureOf(segs[i], deps)
		if segs[i].DepartedHow != "" {
			tr.note("departure", "departure", segs[i].Start, segs[i].End, "left at %s by %s, the best evidence in its span", segs[i].Departed.Format("15:04:05"), segs[i].DepartedHow)
		}
	}
	// 9. flights joined by hand
	joins, err := st.FlightJoins(hexAddr, since.Add(-MaxFlight), until)
	if err != nil {
		return nil, err
	}
	segs = applyJoins(segs, joins, tr)
	// 10. a flight no sighting named, whose messages airframes.io all tagged with one number, is shown under it. This only labels: it is
	// not a sighting, so it never starts, extends or joins a flight, and the flight stays unidentified.
	for i := range segs {
		if sg := &segs[i]; !sg.Identified {
			if id, n, ok := taggedFlight(st, hexAddr, sg.Start, sg.End); ok {
				sg.Flight, sg.Callsign, sg.FlightFromAF = id.iata, id.icao, true
				if sg.Flight == "" {
					sg.Flight = id.display
				}
				tr.note("label", "tagged", sg.Start, sg.End, "no sighting named it; airframes.io tagged %d of its messages %s and no other number", n, id.display)
			}
		}
	}
	for i := range segs {
		segs[i].Messages = st.CountMessages(hexAddr, segs[i].Start, segs[i].End)
		segs[i].SeenBy, segs[i].SeenAt = seenBy(segs[i], ev)
		segs[i].ID = segID(segs[i])
	}
	if n := len(segs); n > 0 {
		segs[n-1].Latest = true
	}
	return segs, nil
}

// taggedFlight is the one flight number airframes.io tagged the messages in [from, to] with, if there are at least TagMin sound ones
// and they all agree.
func taggedFlight(st *store.Store, hexAddr string, from, to time.Time) (id ident, n int, ok bool) {
	tags, err := st.AFFlights(hexAddr, from, to)
	if err != nil || len(tags) < TagMin {
		return ident{}, 0, false
	}
	for i, t := range tags {
		x, good := parse(t)
		if !good || (i > 0 && !same(id, x)) {
			return ident{}, 0, false
		}
		id = x
	}
	return id, len(tags), true
}

// splitAtStopovers cuts a flight in two where ADS-B saw the aircraft sit on the ground for StopoverMin or more, between
// its landing and its next departure. The cut is the middle of the stop: what was sighted before it ends the first
// leg and what was sighted after it starts the second, so the paperwork for the next leg (its clearance, its schedule)
// belongs to that leg and the arrival's chatter to the one before.
func splitAtStopovers(segs []Segment, stops []store.GroundStop, ev []store.Evidence, posTimes []time.Time, tr *trace) []Segment {
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
			tr.note("stops", "split", stop.Land, stop.Depart, "%s cut in two at a stop of %v on the ground: the first leg ends at %s, the second starts at %s", sg.Flight, stop.Depart.Sub(stop.Land).Round(time.Minute), lastBefore.Format("15:04:05"), firstAfter.Format("15:04:05"))
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
	if (sg.From == "" || sg.FromSeen) && best.From != "" { // what was entered by hand wins over where the track was seen to start
		sg.From, sg.FromManual, sg.FromSeen = best.From, true, false
	}
	if (sg.To == "" || sg.ToSeen) && best.To != "" { // as for the origin, what was entered by hand wins over what was seen
		sg.To, sg.ToManual, sg.ToSeen = best.To, true, false
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

// routeChoice is the origin and destination reported most often, how many reports said so, and the newest of them.
type routeChoice struct {
	From, To string
	N        int
	At       time.Time // the newest report that said it
	Src      string    // and what kind of report it was
}

// modalRoute is the origin/destination reported most often in [from, to], the latest on a tie.
func modalRoute(rs []store.RouteReport, from, to time.Time) routeChoice {
	type pair struct{ from, to string }
	count := map[pair]int{}
	last := map[pair]store.RouteReport{}
	for _, r := range rs {
		if r.T.Before(from) || r.T.After(to) {
			continue
		}
		p := pair{r.From, r.To}
		count[p]++
		last[p] = r
	}
	var best pair
	n := 0
	for p, c := range count {
		if c > n || (c == n && last[p].T.After(last[best].T)) {
			best, n = p, c
		}
	}
	return routeChoice{From: best.from, To: best.to, N: n, At: last[best].T, Src: last[best].Src}
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

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// seenBy is what carried the last sighting of the flight's number inside it: the newest sighting that is of that flight and falls
// in its span.
func seenBy(sg Segment, ev []store.Evidence) (string, time.Time) {
	if !sg.Identified {
		return "", time.Time{}
	}
	want, ok := parse(sg.Flight)
	if !ok {
		return "", time.Time{}
	}
	for i := len(ev) - 1; i >= 0; i-- {
		e := ev[i]
		if e.T.After(sg.End) || e.T.Before(sg.Start.Add(-AttachSlack)) {
			continue
		}
		if id, ok := parse(e.ID); ok && same(id, want) {
			return e.Src, e.T
		}
	}
	return "", time.Time{}
}

// formsText lists the ways a flight number was written, most used first: "CX113 x120, CPA113 x30".
func formsText(forms map[string]int) string {
	names := make([]string, 0, len(forms))
	for f := range forms {
		names = append(names, f)
	}
	sort.Slice(names, func(i, j int) bool {
		if forms[names[i]] != forms[names[j]] {
			return forms[names[i]] > forms[names[j]]
		}
		return names[i] < names[j]
	})
	for i, f := range names {
		names[i] = fmt.Sprintf("%s x%d", f, forms[f])
	}
	return strings.Join(names, ", ")
}

// joinReason says why joinable took b to be a piece of a.
func joinReason(a, b Segment) string {
	if a.From != "" && a.To != "" && a.From == b.From && a.To == b.To {
		return fmt.Sprintf("both report %s > %s, %v apart", a.From, a.To, b.Start.Sub(a.End).Round(time.Minute))
	}
	return fmt.Sprintf("an unnumbered piece %v from a numbered flight with no route of its own", b.Start.Sub(a.End).Round(time.Minute))
}

// notJoinedReason says why joinable kept b apart from a, two pieces of one flight number.
func notJoinedReason(a, b Segment) string {
	switch {
	case a.stopAfter:
		return "the aircraft stopped on the ground between them: two legs"
	case b.End.Sub(a.Start) > MaxFlight:
		return fmt.Sprintf("together they would run %v, over %v", b.End.Sub(a.Start).Round(time.Minute), MaxFlight)
	case a.From == "" || a.To == "" || b.From == "" || b.To == "":
		return fmt.Sprintf("no route reported for both (%q > %q and %q > %q), %v apart", a.From, a.To, b.From, b.To, b.Start.Sub(a.End).Round(time.Minute))
	case a.From != b.From || a.To != b.To:
		return fmt.Sprintf("they report different routes (%s > %s and %s > %s)", a.From, a.To, b.From, b.To)
	default:
		return fmt.Sprintf("%v apart, over the %v allowed between pieces of one route", b.Start.Sub(a.End).Round(time.Minute), MaxRouteGap)
	}
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
	out.Pending = a.Pending && b.Pending
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
func setAsideStale(ev []store.Evidence, tr *trace) []store.Evidence {
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
		tr.note("evidence", "stale", ev[i].T, ev[k-1].T, "%d sighting(s) of %s after %v of silence, then %s within %v and %d more of it: the old number sent again at power-up, not the end of the old flight",
			k-i, ev[i].ID, ev[i].T.Sub(ev[i-1].T).Round(time.Minute), ev[k].ID, StaleWithin, followers)
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
func setAsideWeak(ev []store.Evidence, tr *trace) []store.Evidence {
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
			tr.note("evidence", "weak", e.T, e.T, "airline message naming %s set aside: the aircraft itself named another flight within %v before and again after", e.ID, WeakWithin)
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
func bridgeSilences(runs []*run, fixes []store.AirFix, tr *trace) []*run {
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
			refs := []float64{speedAt(p, -1), speedAt(p+1, 1)}
			for _, ref := range refs {
				if ref > 0 && kt >= ref*(1-BridgeSpeedTolerance) && kt <= ref*(1+BridgeSpeedTolerance) {
					ok = true
				}
			}
			if !ok || kt < BridgeMinKt || kt > BridgeMaxKt {
				continue
			}
			tr.note("runs", "bridge", a.last, b.first, "%s joined across %v of silence: airborne at both ends, %.0f kt between the fixes, against %.0f kt before and %.0f kt after", a.id.display, gap.Round(time.Minute), kt, refs[0], refs[1])
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
func applyJoins(segs []Segment, joins []time.Time, tr *trace) []Segment {
	for k := len(joins) - 1; k >= 0; k-- { // latest first, so earlier positions stay where they were
		at := joins[k]
		for i := 1; i < len(segs); i++ {
			if at.Before(segs[i].Start.Add(-manualJoinSlack)) || at.After(segs[i].End) {
				continue
			}
			a, b := segs[i-1], segs[i]
			tr.note("manual", "manual-join", a.Start, b.End, "joined by hand: the piece starting %s is part of the flight before it", b.Start.Format("15:04:05"))
			m := a
			m.End = b.End
			m.Positions += b.Positions
			if m.From == "" {
				m.From, m.FromManual = b.From, b.FromManual
			}
			if b.To != "" {
				m.To, m.ToManual, m.RouteSource, m.RouteAt = b.To, b.ToManual, b.RouteSource, b.RouteAt
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

// An unseenStop is a stop on the ground that no position showed: at Airport, from Land (when the aircraft could have got there) to
// Depart (when it was seen climbing away).
type unseenStop struct {
	store.GroundStop
	Airport string
}

// unseenStops finds the stops on the ground that the fixes only imply: one fix long after the one before, low, climbing, and next to
// an airport the aircraft cannot have been at before.
func unseenStops(fixes []store.AirFix) []unseenStop {
	var out []unseenStop
	for k := 1; k < len(fixes); k++ {
		prev, f := fixes[k-1], fixes[k]
		if f.T.Sub(prev.T) < unseenApproachGap {
			continue
		}
		if f.OnGround {
			// ...or the first fix after the silence is on the ground at the airport the one before was coming in to (N956AK's AS485
			// into Seattle, last heard on final, then ADS-B picking it up at the gate as AS421): it landed, and the stop lasts at
			// least until that fix.
			if prev.OnGround || prev.AltFt == nil {
				continue
			}
			hits := airports.Near(f.Lat, f.Lon, departedNM)
			if len(hits) == 0 {
				continue
			}
			away := geo.DistNM(prev.Lat, prev.Lon, hits[0].Lat, hits[0].Lon)
			if !approach(fixes, k-1, away) {
				continue
			}
			land := prev.T.Add(time.Duration(away / approachKt * float64(time.Hour)))
			out = append(out, unseenStop{store.GroundStop{Land: land, Depart: f.T}, hits[0].ICAO})
			continue
		}
		if f.AltFt == nil || *f.AltFt > unseenClimbFt {
			continue
		}
		climbing := false
		for j := k + 1; j < len(fixes) && fixes[j].T.Sub(f.T) <= 10*time.Minute; j++ {
			if fixes[j].AltFt != nil && *fixes[j].AltFt >= *f.AltFt+2000 {
				climbing = true
				break
			}
		}
		hits := airports.Near(f.Lat, f.Lon, unseenNearNM)
		if !climbing || len(hits) == 0 {
			continue
		}
		a := hits[0]
		away := geo.DistNM(prev.Lat, prev.Lon, a.Lat, a.Lon)
		land := prev.T.Add(time.Duration(away / UnseenCruiseKt * float64(time.Hour))) // (a stop shorter than StopoverMin does not split anything)
		switch {
		case away < unseenFromNM:
			if !approach(fixes, k-1, away) {
				continue
			}
			land = prev.T.Add(time.Duration(away / approachKt * float64(time.Hour)))
		case f.T.Sub(prev.T) < UnseenStopGap:
			continue
		}
		out = append(out, unseenStop{store.GroundStop{Land: land, Depart: f.T}, a.ICAO})
	}
	return out
}

// approach reports whether fixes[i], away nm from the airport, was an aircraft coming in to it: in the air, low, close, and lower than
// a fix in the ten minutes before; or, where fixes are sparse (an FMS report every fifteen minutes), simply very low and very close.
func approach(fixes []store.AirFix, i int, away float64) bool {
	f := fixes[i]
	if f.OnGround || f.AltFt == nil {
		return false
	}
	if *f.AltFt <= onFinalFt && away <= onFinalNM {
		return true
	}
	if *f.AltFt > unseenApproachFt || away > unseenApproachNM {
		return false
	}
	for j := i - 1; j >= 0 && f.T.Sub(fixes[j].T) <= 10*time.Minute; j-- {
		if fixes[j].AltFt != nil && *fixes[j].AltFt >= *f.AltFt+500 {
			return true
		}
	}
	return false
}

// leftFrom is the airport an aircraft was seen to leave in [from, to]: its first fix there is on the ground within departedNM of
// an airport, and a later one is in the air.
func leftFrom(fixes []store.AirFix, from, to time.Time) (string, bool) {
	for k, f := range fixes {
		if f.T.Before(from) {
			continue
		}
		if f.T.After(to) || !f.OnGround {
			return "", false
		}
		hits := airports.Near(f.Lat, f.Lon, departedNM)
		if len(hits) == 0 {
			return "", false
		}
		for _, g := range fixes[k+1:] {
			if g.T.After(to) {
				break
			}
			if !g.OnGround {
				return hits[0].ICAO, true
			}
		}
		return "", false
	}
	return "", false
}

// landedAt is the airport the flight ended at, if ADS-B's last fix in the span is on the ground next to one.
func landedAt(fixes []store.AirFix, from, to time.Time) (string, bool) {
	var last *store.AirFix
	flew := false // a flight still at its gate has only ground fixes, and is leaving from there
	for k := range fixes {
		if f := &fixes[k]; !f.T.Before(from) && !f.T.After(to) {
			last = f
			flew = flew || !f.OnGround
		}
	}
	if last == nil || !last.OnGround || !flew {
		return "", false
	}
	if hits := airports.Near(last.Lat, last.Lon, departedNM); len(hits) > 0 {
		return hits[0].ICAO, true
	}
	return "", false
}

// SameFlight reports whether two flight numbers or callsigns name one flight (CX833, CX0833 and CPA833).
func SameFlight(a, b string) bool {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	return ok1 && ok2 && same(x, y)
}

// Elsewhere finds the flight a message belongs to when it is tagged with a flight number (by airframes.io, from the message's own
// header) that is not the flight of ours that holds its time: the earlier flight of that number, if we have it. A message with
// nothing to say for itself but its arrival time (an ATIS request, a CPDLC exchange) lands in whichever flight was in progress when
// it arrived; one delivered late by a ground station, or sent before the flight number was changed after landing, lands in the wrong
// one. t is when the message counts, tagged the flight number it carries. The flight must have ended more than HoldTolerance
// before t, so a message at the very end of its own flight is not moved.
func Elsewhere(segs []Segment, t time.Time, tagged string) (Segment, bool) {
	holder := -1
	var best time.Duration
	for i, s := range segs {
		d := time.Duration(0)
		if t.Before(s.Start) {
			d = s.Start.Sub(t)
		} else if t.After(s.End) {
			d = t.Sub(s.End)
		}
		if d <= HoldTolerance && (holder < 0 || d < best) {
			holder, best = i, d
		}
	}
	if holder < 0 || !segs[holder].Identified || SameFlight(tagged, segs[holder].Flight) {
		return Segment{}, false
	}
	for _, s := range segs {
		if SameFlight(tagged, s.Flight) && s.Start.After(t) && s.Start.Sub(t) <= UpcomingWithin {
			return Segment{}, false // the same number flies again soon: this may be about the flight to come
		}
	}
	for i := len(segs) - 1; i >= 0; i-- {
		if i != holder && SameFlight(tagged, segs[i].Flight) && segs[i].End.Add(HoldTolerance).Before(t) {
			return segs[i], true
		}
	}
	return Segment{}, false
}

// UpcomingWithin is how soon a flight of the same number must follow for a message tagged with it to be taken as possibly about that
// one (an airline sends the next flight's data while the aircraft is on its way in), and so left alone.
const UpcomingWithin = 36 * time.Hour

// HoldTolerance is how far outside a flight's span a time is still taken to be in it (the page uses the same twenty minutes).
const HoldTolerance = 20 * time.Minute
