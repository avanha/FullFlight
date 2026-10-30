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
)

type Segment struct {
	ID         string    `json:"id"`
	Flight     string    `json:"flight,omitempty"`   // IATA form where we know the airline, e.g. CX113
	Callsign   string    `json:"callsign,omitempty"` // ICAO form, e.g. CPA113
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Messages   int       `json:"messages"`
	Positions  int       `json:"positions"`
	From       string    `json:"from,omitempty"` // from REP081, where the aircraft sends one
	To         string    `json:"to,omitempty"`
	Identified bool      `json:"identified"` // false: a flight whose number we never saw
	Latest     bool      `json:"latest"`     // the most recent flight
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

	// 6. the route the aircraft reported; pieces of one flight that share it are joined
	routes, err := st.RouteReports(hexAddr, since.Add(-RouteSlack), until.Add(RouteSlack))
	if err != nil {
		return nil, err
	}
	setRoute := func(s *Segment) { s.From, s.To = modalRoute(routes, s.Start.Add(-RouteSlack), s.End.Add(RouteSlack)) }
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
	for i := range segs {
		segs[i].Messages = st.CountMessages(hexAddr, segs[i].Start, segs[i].End)
		segs[i].ID = segID(segs[i])
	}
	if n := len(segs); n > 0 {
		segs[n-1].Latest = true
	}
	return segs, nil
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
