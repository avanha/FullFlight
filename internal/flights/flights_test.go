package flights

import (
	"fmt"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

const hex = "780AB8"

var day0 = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

func at(d, h, m int) time.Time {
	return day0.Add(time.Duration(d)*24*time.Hour + time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

type rig struct {
	t  *testing.T
	st *store.Store
	id int64
}

func newRig(t *testing.T) *rig {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &rig{t: t, st: st}
}

func (r *rig) msg(m store.Message) {
	r.id++
	m.ID, m.Hex, m.Text = r.id, hex, fmt.Sprint("t", r.id)
	if m.Label == "" {
		m.Label = "BA"
	}
	if _, err := r.st.AddMessage(m); err != nil {
		r.t.Fatal(err)
	}
}

// flight number seen in a message at t (no position)
func (r *rig) said(t time.Time, flight string) { r.msg(store.Message{T: t, Flight: flight}) }

// a datalink position at t
func (r *rig) fix(t time.Time) {
	lat, lon := 10.0, 100.0
	r.msg(store.Message{T: t, Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &t})
}

// an ADS-B position at t with a callsign (may be empty)
func (r *rig) adsb(t time.Time, callsign string) {
	r.st.AddPosition(store.Position{Hex: hex, T: t, Lat: 10 + float64(t.Unix()%1000)/1000, Lon: 100, Callsign: callsign, Source: "x"})
}

func (r *rig) route(t time.Time, from, to string) {
	pt := t
	r.msg(store.Message{T: t, Kind: "miam", PosT: &pt, Decoded: `{"fields":{"rep081":{"Origin":"` + from + `","Dest":"` + to + `"}}}`})
}

func (r *rig) segments() []Segment {
	r.t.Helper()
	segs, err := Segments(r.st, hex, day0.Add(-24*time.Hour), day0.Add(30*24*time.Hour))
	if err != nil {
		r.t.Fatal(err)
	}
	return segs
}

func names(segs []Segment) string {
	s := ""
	for _, x := range segs {
		s += x.Flight + "|"
	}
	return s
}

func TestParseAndSame(t *testing.T) {
	for _, bad := range []string{"", "CX0", "CX000", "X", "123456", "CPA", "12"} {
		if _, ok := parse(bad); ok {
			t.Errorf("%q should not be a flight", bad)
		}
	}
	// a general-aviation aircraft's callsign is its registration; that is as good an identity as any
	if ga, ok := parse("N12345"); !ok || ga.display != "N12345" {
		t.Errorf("N12345 = %+v %v", ga, ok)
	}
	if x, ok := parse("6E123"); !ok || x.airline != "IGO" && x.airline != "6E" {
		t.Errorf("6E123 = %+v %v", x, ok)
	}
	a, _ := parse("CPA0113")
	b, _ := parse("cx113")
	c, _ := parse("CX0113")
	d, _ := parse("CX114")
	if !same(a, b) || !same(b, c) || same(a, d) {
		t.Errorf("CPA0113 / cx113 / CX0113 are one flight, CX114 another: %v %v %v", same(a, b), same(b, c), same(a, d))
	}
	if a.iata != "CX113" || a.icao != "CPA113" || a.display != "CX113" {
		t.Errorf("forms = %+v", a)
	}
	// UPS flight 65 is "UP65" in its messages, "UPS65" as a callsign and 5X65 in the schedule: one flight
	up1, _ := parse("UP65")
	up2, _ := parse("UPS0065")
	up3, _ := parse("5X65")
	up4, _ := parse("UP66")
	if !same(up1, up2) || !same(up2, up3) || same(up1, up4) || up1.display != "5X65" {
		t.Errorf("UP65 / UPS0065 / 5X65: %v %v %v display %q", same(up1, up2), same(up2, up3), same(up1, up4), up1.display)
	}
	// an airline we have no table entry for: the 3- and 2-letter codes with one initial are taken as one
	u1, _ := parse("ABC123")
	u2, _ := parse("AB123")
	u3, _ := parse("XB123")
	if !same(u1, u2) || same(u1, u3) {
		t.Errorf("unmapped airline matching: %v %v", same(u1, u2), same(u1, u3))
	}
}

// spread calls f every step minutes from start for length.
func spread(start time.Time, length, step time.Duration, f func(time.Time)) {
	for d := time.Duration(0); d <= length; d += step {
		f(start.Add(d))
	}
}

// Two flights the same day: the callsign on ADS-B is in ICAO form, the one in messages in IATA form,
// and they are still one flight each.
func TestTwoFlights(t *testing.T) {
	r := newRig(t)
	spread(at(0, 2, 0), 5*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX0113"); r.fix(x.Add(time.Minute)) })
	spread(at(0, 3, 0), 4*time.Hour, 5*time.Minute, func(x time.Time) { r.adsb(x, "CPA113") })
	spread(at(0, 14, 0), 6*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX840"); r.fix(x.Add(time.Minute)) })
	segs := r.segments()
	if len(segs) != 2 {
		t.Fatalf("segments = %+v", segs)
	}
	a, b := segs[0], segs[1]
	if a.Flight != "CX113" || a.Callsign != "CPA113" || !a.Identified || a.Latest {
		t.Errorf("first = %+v", a)
	}
	if b.Flight != "CX840" || b.Callsign != "CPA840" || !b.Latest {
		t.Errorf("second = %+v", b)
	}
	if !a.Start.Equal(at(0, 2, 0)) || !a.End.Equal(at(0, 7, 1)) {
		t.Errorf("first runs %v to %v", a.Start, a.End)
	}
	if a.Positions != 11+49 || a.Messages < 11 { // 11 datalink fixes + 49 ADS-B points
		t.Errorf("first counts: %d positions, %d messages", a.Positions, a.Messages)
	}
	if a.ID == b.ID || a.ID != fmt.Sprintf("CX113@%d", a.Start.Unix()) {
		t.Errorf("ids = %q %q", a.ID, b.ID)
	}
}

// The same flight number on different days is different flights: the silence between is far over MaxGap.
func TestSameNumberOnTwoDays(t *testing.T) {
	r := newRig(t)
	for d := 0; d < 3; d++ {
		spread(at(d, 4, 0), 9*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX833"); r.fix(x) })
	}
	segs := r.segments()
	if len(segs) != 3 || names(segs) != "CX833|CX833|CX833|" {
		t.Fatalf("segments = %+v", segs)
	}
	if segs[0].ID == segs[1].ID {
		t.Error("distinct flights share an id")
	}
}

// One mislabelled message inside a flight does not cut it in two, and a stray alone on the ground is dropped.
func TestStraysAreFoldedAway(t *testing.T) {
	r := newRig(t)
	spread(at(0, 2, 0), 6*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "CX111"); r.fix(x) })
	r.said(at(0, 5, 0), "CX999") // one message of another number, in the middle
	r.said(at(0, 5, 0), "CX0")   // and a broken one
	segs := r.segments()
	if len(segs) != 1 || segs[0].Flight != "CX111" || !segs[0].Start.Equal(at(0, 2, 0)) || !segs[0].End.Equal(at(0, 8, 0)) {
		t.Fatalf("one flight expected, got %+v", segs)
	}
	// the pre-departure stray before the next flight has no position and is dropped, not shown as a flight
	r.said(at(1, 10, 29), "CX833")
	spread(at(1, 10, 37), 7*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "CX843"); r.fix(x) })
	if got := names(r.segments()); got != "CX111|CX843|" {
		t.Errorf("flights = %s", got)
	}
}

// Positions near a flight's sightings belong to it; positions that name nothing but cluster are a flight of
// unknown number; one lonely position is nothing.
func TestPositionsWithoutANumber(t *testing.T) {
	r := newRig(t)
	spread(at(0, 10, 0), 4*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX105"); r.fix(x) })
	r.fix(at(0, 9, 20))                                                              // 40 minutes before its first sighting: still that flight
	r.fix(at(0, 15, 10))                                                             // an hour and ten after its last (14:00): too far
	spread(at(2, 4, 0), 2*time.Hour, 30*time.Minute, func(x time.Time) { r.fix(x) }) // five positions, no number anywhere
	r.fix(at(3, 20, 0))                                                              // one position on its own
	segs := r.segments()
	if len(segs) != 2 {
		t.Fatalf("segments = %+v", segs)
	}
	if !segs[0].Start.Equal(at(0, 9, 20)) || segs[0].Positions != 9+1 { // 9 fixes while sighted + the one 40 minutes early; the 15:10 one is orphaned
		t.Errorf("first = %+v", segs[0])
	}
	u := segs[1]
	if u.Identified || u.Flight != "" || u.Positions != 5 || u.ID != fmt.Sprintf("?@%d", at(2, 4, 0).Unix()) || !u.Latest {
		t.Errorf("unidentified = %+v", u)
	}
}

func TestRouteFromREP081(t *testing.T) {
	r := newRig(t)
	spread(at(0, 2, 0), 6*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX110"); r.fix(x) })
	r.route(at(0, 3, 0), "VHHH", "EDDM")
	r.route(at(0, 4, 0), "VHHH", "EDDM")
	r.route(at(0, 5, 0), "VHHH", "LEBL") // an odd one out
	spread(at(1, 2, 0), 6*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX111"); r.fix(x) })
	r.route(at(1, 3, 0), "EDDM", "VHHH")
	segs := r.segments()
	if segs[0].From != "VHHH" || segs[0].To != "EDDM" {
		t.Errorf("first route = %s -> %s, want the most reported", segs[0].From, segs[0].To)
	}
	if segs[1].From != "EDDM" || segs[1].To != "VHHH" {
		t.Errorf("second route = %s -> %s", segs[1].From, segs[1].To)
	}
	r2 := newRig(t)
	spread(at(0, 2, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r2.said(x, "UA869"); r2.fix(x) })
	if s := r2.segments(); s[0].From != "" || s[0].To != "" {
		t.Errorf("a route from nowhere: %+v", s[0])
	}
}

func TestNothingAndOtherAircraft(t *testing.T) {
	r := newRig(t)
	if segs := r.segments(); len(segs) != 0 {
		t.Errorf("segments from nothing: %+v", segs)
	}
	// another aircraft's sightings and positions never count
	other := &store.Message{ID: 900, Hex: "AAAAAA", T: at(0, 3, 0), Label: "BA", Text: "z", Flight: "UA1"}
	r.st.AddMessage(*other)
	r.st.AddPosition(store.Position{Hex: "AAAAAA", T: at(0, 3, 0), Lat: 1, Lon: 1, Callsign: "UAL1", Source: "x"})
	if segs := r.segments(); len(segs) != 0 {
		t.Errorf("another aircraft leaked in: %+v", segs)
	}
	// a message whose text names a different aircraft does not give this one its flight
	lat, lon := 1.0, 1.0
	pt := at(0, 3, 0)
	r.msg(store.Message{T: pt, Flight: "CX999", Mismatch: "text names B-LRD, which is hex 780AA4, not 780AB8", Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &pt})
	if got := names(r.segments()); got != "" { // the position is there, the flight number is not: an unidentified flight needs two
		t.Errorf("flights = %q", got)
	}
}

// A flight whose number was only logged near its end, with its first hours reported as bare positions:
// the REP081 route they share joins them into one flight.
func TestPiecesOfOneFlightAreJoinedByRoute(t *testing.T) {
	r := newRig(t)
	// EGCC -> VHHH: two unnumbered clusters, then the numbered part
	spread(at(0, 11, 0), 90*time.Minute, 30*time.Minute, func(x time.Time) { r.fix(x) })
	r.route(at(0, 11, 30), "EGCC", "VHHH")
	spread(at(0, 15, 0), 2*time.Hour, 30*time.Minute, func(x time.Time) { r.fix(x) })
	r.route(at(0, 16, 0), "EGCC", "VHHH")
	spread(at(0, 18, 30), 5*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX216"); r.fix(x) })
	r.route(at(0, 20, 0), "EGCC", "VHHH")
	segs := r.segments()
	if len(segs) != 1 {
		t.Fatalf("want one flight, got %d: %+v", len(segs), segs)
	}
	s := segs[0]
	if s.Flight != "CX216" || !s.Identified || !s.Start.Equal(at(0, 11, 0)) || !s.End.Equal(at(0, 23, 30)) || s.From != "EGCC" || s.To != "VHHH" {
		t.Errorf("flight = %+v", s)
	}
	if s.ID != fmt.Sprintf("CX216@%d", at(0, 11, 0).Unix()) {
		t.Errorf("id = %q: the id follows the merged start", s.ID)
	}
	if s.Positions != 4+5+11 {
		t.Errorf("positions = %d", s.Positions)
	}
}

func TestPiecesAreNotJoinedWhenTheyShouldNotBe(t *testing.T) {
	// different routes: the return leg right after arrival is another flight
	r := newRig(t)
	spread(at(0, 2, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.fix(x) })
	r.route(at(0, 3, 0), "VHHH", "YMML")
	spread(at(0, 10, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX104"); r.fix(x) })
	r.route(at(0, 11, 0), "YMML", "VHHH")
	if got := r.segments(); len(got) != 2 {
		t.Errorf("a route and its reverse joined: %+v", got)
	}
	// two numbered flights with the same route are never joined (even close together)
	r = newRig(t)
	spread(at(0, 2, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX100"); r.fix(x) })
	r.route(at(0, 3, 0), "VHHH", "YSSY")
	spread(at(0, 9, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX200"); r.fix(x) })
	r.route(at(0, 10, 0), "VHHH", "YSSY")
	if got := r.segments(); len(got) != 2 {
		t.Errorf("different numbered flights joined: %+v", got)
	}
	// the same route but a day apart: not joined (over MaxRouteGap)
	r = newRig(t)
	spread(at(0, 2, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.fix(x) })
	r.route(at(0, 3, 0), "VHHH", "YSSY")
	spread(at(1, 6, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX110"); r.fix(x) })
	r.route(at(1, 7, 0), "VHHH", "YSSY")
	if got := r.segments(); len(got) != 2 {
		t.Errorf("flights a day apart joined: %+v", got)
	}
}

// An unnumbered piece right beside a numbered flight with no route to compare is that flight's start.
func TestUnnumberedPieceAdoptedOnlyWhenClose(t *testing.T) {
	build := func(gap time.Duration) []Segment {
		r := newRig(t)
		spread(at(0, 6, 0), 20*time.Minute, 10*time.Minute, func(x time.Time) { r.fix(x) }) // 06:00-06:20, no number
		spread(at(0, 6, 20).Add(gap), 5*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX852"); r.fix(x) })
		return r.segments()
	}
	if got := build(100 * time.Minute); len(got) != 1 || got[0].Flight != "CX852" || !got[0].Start.Equal(at(0, 6, 0)) {
		t.Errorf("101 minutes: %+v", got)
	}
	if got := build(3 * time.Hour); len(got) != 2 {
		t.Errorf("3 hours: %+v", got)
	}
}

// When a flight left comes from the best evidence there is: a takeoff report, else ADS-B seeing it leave the ground, else
// a time at the gate.
func TestDeparture(t *testing.T) {
	r := newRig(t)
	// day 0: a takeoff report at 02:30 for 02:26, and an ADS-B takeoff at 02:50 that the report outranks
	spread(at(0, 2, 30), 4*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX110"); r.fix(x) })
	r.msg(store.Message{T: at(0, 2, 30), Kind: "miam", Decoded: `{"fields":{"takeoff":{"Origin":"VHHH","Dest":"EDDM","Off":"0226"}}}`})
	r.st.AddPosition(store.Position{Hex: hex, T: at(0, 2, 45), Lat: 22.3, Lon: 113.9, OnGround: true, Source: "x"})
	r.st.AddPosition(store.Position{Hex: hex, T: at(0, 2, 50), Lat: 22.4, Lon: 113.9, Source: "x"})
	// day 1: ADS-B alone
	spread(at(1, 2, 0), 4*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX111"); r.fix(x) })
	r.st.AddPosition(store.Position{Hex: hex, T: at(1, 2, 20), Lat: 22.3, Lon: 113.9, OnGround: true, Source: "x"})
	r.st.AddPosition(store.Position{Hex: hex, T: at(1, 2, 24), Lat: 22.4, Lon: 113.9, Source: "x"})
	// day 2: only a departure report from the gate, sent just after midnight about a push back just before it
	spread(at(2, 0, 10), 4*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX112"); r.fix(x) })
	r.msg(store.Message{T: at(2, 0, 3), Kind: "departure_report", Decoded: `{"fields":{"departure":{"Events":{"out":{"OUT EVENT":"235800"},"taxi":{"MOVEMENT":"000100"}}}}}`})
	// day 3: nothing
	spread(at(3, 2, 0), 4*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "CX113"); r.fix(x) })

	segs := r.segments()
	if len(segs) != 4 {
		t.Fatalf("segments = %s", names(segs))
	}
	want := []struct {
		t   time.Time
		how string
	}{{at(0, 2, 26), "takeoff"}, {at(1, 2, 24), "adsb"}, {at(1, 23, 58), "pushback"}, {time.Time{}, ""}}
	for i, w := range want {
		if !segs[i].Departed.Equal(w.t) || segs[i].DepartedHow != w.how {
			t.Errorf("%s departed %v (%s), want %v (%s)", segs[i].Flight, segs[i].Departed, segs[i].DepartedHow, w.t, w.how)
		}
	}
}

// A route typed in by hand fills what the messages never said, stays on the flight it was entered for,
// and gives way to a route the messages do give.
func TestManualRoute(t *testing.T) {
	r := newRig(t)
	spread(at(0, 2, 0), 6*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "UA869"); r.fix(x) })
	spread(at(1, 2, 0), 6*time.Hour, 30*time.Minute, func(x time.Time) { r.said(x, "UA870"); r.fix(x) })
	segs := r.segments()
	if len(segs) != 2 || segs[0].From != "" || segs[0].To != "" {
		t.Fatalf("setup: %+v", segs)
	}
	enter := func(sg Segment, from, to string) {
		if err := r.st.SetManualRoute(hex, sg.Start, sg.End, from, to, at(2, 0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	enter(segs[0], "KSFO", "VHHH")
	segs = r.segments()
	if s := segs[0]; s.From != "KSFO" || s.To != "VHHH" || !s.FromManual || !s.ToManual {
		t.Errorf("first flight after entry: %+v", s)
	}
	if s := segs[1]; s.From != "" || s.To != "" || s.FromManual || s.ToManual {
		t.Errorf("the entry spilled onto the next flight: %+v", s)
	}

	// Entering again replaces it; one end alone is allowed.
	enter(segs[0], "", "RJTT")
	if s := r.segments()[0]; s.From != "" || s.To != "RJTT" || s.FromManual || !s.ToManual {
		t.Errorf("after replacing: %+v", s)
	}

	// A destination the messages give wins; the origin they lack is still filled in.
	r.route(at(0, 3, 0), "", "EDDM")
	enter(segs[0], "KSFO", "RJTT")
	if s := r.segments()[0]; s.To != "EDDM" || s.ToManual || s.From != "KSFO" || !s.FromManual {
		t.Errorf("data should win over the entry for the destination: %+v", s)
	}

	// Clearing removes it.
	enter(segs[0], "", "")
	if s := r.segments()[0]; s.From != "" || s.FromManual {
		t.Errorf("after clearing: %+v", s)
	}
}

// ground is an ADS-B position on the ground at t.
func (r *rig) ground(t time.Time) {
	r.st.AddPosition(store.Position{Hex: hex, T: t, Lat: 61.17, Lon: -150.0, OnGround: true, Callsign: "UPS65", Source: "x"})
}

// An aircraft that lands, sits for hours, and flies on under the same flight number has flown two flights.
func TestStopoverSplitsAFlight(t *testing.T) {
	r := newRig(t)
	// leg 1: airborne 02:00 to 05:00, with messages; then it lands and sits on the ground until 08:00, chatting
	spread(at(0, 2, 0), 3*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "UP65"); r.adsb(x, "UPS65") })
	r.route(at(0, 4, 0), "VHHH", "PANC")
	spread(at(0, 5, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r.ground(x); r.said(x.Add(time.Minute), "UP65") })
	// leg 2: airborne again from 08:10 to 12:00; its route is reported once it is under way
	spread(at(0, 8, 10), 4*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "UP65"); r.adsb(x, "UPS65") })
	r.route(at(0, 9, 0), "PANC", "KSDF")
	segs := r.segments()
	if len(segs) != 2 {
		t.Fatalf("want two legs, got %s", names(segs))
	}
	a, b := segs[0], segs[1]
	if a.Flight != "5X65" || b.Flight != "5X65" || a.ID == b.ID {
		t.Errorf("both legs keep the number but have their own ids: %s, %s", a.ID, b.ID)
	}
	if a.From != "VHHH" || a.To != "PANC" || b.From != "PANC" || b.To != "KSDF" {
		t.Errorf("each leg has its own route: %s->%s, %s->%s", a.From, a.To, b.From, b.To)
	}
	// the stop runs from the landing at 05:00 to the next airborne position at 08:10; the cut is half way, 06:35, and the
	// arrival's chatter before it belongs to the first leg
	if a.End.After(at(0, 6, 35)) || !a.End.After(at(0, 6, 0)) || !b.Start.After(at(0, 6, 35)) || b.Start.After(at(0, 7, 35)) {
		t.Errorf("legs meet around the middle of the stop: first ends %s, second starts %s", a.End.Format("15:04"), b.Start.Format("15:04"))
	}
	if a.Latest || !b.Latest {
		t.Errorf("only the second is the latest: %v %v", a.Latest, b.Latest)
	}
	if a.Positions == 0 || b.Positions == 0 {
		t.Errorf("positions: %d, %d", a.Positions, b.Positions)
	}
}

// A short stop, or one ADS-B did not see both ends of, leaves one flight; so does a stop with no flight number around it.
func TestShortStopDoesNotSplit(t *testing.T) {
	r := newRig(t)
	spread(at(0, 2, 0), 2*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "UP65"); r.adsb(x, "UPS65") })
	spread(at(0, 4, 0), 30*time.Minute, 10*time.Minute, func(x time.Time) { r.ground(x); r.said(x, "UP65") }) // a 30 minute stop
	spread(at(0, 4, 40), 2*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "UP65"); r.adsb(x, "UPS65") })
	if segs := r.segments(); len(segs) != 1 {
		t.Errorf("a short stop is not a new flight: %s", names(segs))
	}

	// landed, and the aircraft was not heard in the air again within the window: no second leg to cut
	r2 := newRig(t)
	spread(at(0, 2, 0), 2*time.Hour, 20*time.Minute, func(x time.Time) { r2.said(x, "UP65"); r2.adsb(x, "UPS65") })
	spread(at(0, 4, 0), 3*time.Hour, 30*time.Minute, func(x time.Time) { r2.ground(x); r2.said(x, "UP65") })
	if segs := r2.segments(); len(segs) != 1 {
		t.Errorf("a stop with no departure yet is one flight: %s", names(segs))
	}
}

// Qantas sends the next flight's data while the aircraft is still on its way in: QF64 was landing at Sydney when QF1's
// flight data (scheduled two hours later) arrived. Those messages belong to QF1, from the time it is due to leave: they
// must not cut QF64 in two or give it QF1's route.
func TestNextFlightsDataBeforeLandingDoesNotMuddleTheCurrentOne(t *testing.T) {
	r := newRig(t)
	sched := at(0, 5, 30)
	next := func(x time.Time) {
		r.msg(store.Message{T: x, Kind: "ops_report", Flight: "QF1", Callsign: "QFA1", Label: "RF",
			Decoded: `{"fields":{"ops":{"event":"FDACOM","origin":"YSSY","dest":"WSSS","flight":"QF1","sched":"` + sched.Format(time.RFC3339) + `"}}}`})
	}
	spread(at(0, 0, 0), 3*time.Hour+50*time.Minute, 10*time.Minute, func(x time.Time) { r.said(x, "QF64"); r.fix(x) }) // lands 03:50
	next(at(0, 3, 26))
	next(at(0, 3, 31))
	next(at(0, 3, 44))
	r.route(at(0, 3, 40), "FAOR", "YSSY")

	until := at(0, 4, 30) // the aircraft is at the gate; QF1 has not left
	segs, err := Segments(r.st, hex, day0.Add(-24*time.Hour), until)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Flight != "QF64" || segs[0].From != "FAOR" || segs[0].To != "YSSY" {
		t.Fatalf("before QF1 leaves: %s %+v", names(segs), segs)
	}

	// once it has gone, QF1 is its own flight, with the route its data gave
	spread(at(0, 5, 40), 90*time.Minute, 10*time.Minute, func(x time.Time) { r.said(x, "QF1"); r.fix(x) })
	segs, err = Segments(r.st, hex, day0.Add(-24*time.Hour), at(0, 8, 0))
	if err != nil {
		t.Fatal(err)
	}
	if names(segs) != "QF64|QF1|" || segs[1].From != "YSSY" || segs[1].To != "WSSS" || segs[0].To != "YSSY" {
		t.Errorf("after: %s %+v", names(segs), segs)
	}
}

// United's engine reports carry the time of their event and are often delivered days late. The flight they name belongs
// where it happened, not where they arrived, or its positions sit in a flight with no name.
func TestLateReportsCountFromTheirEventTime(t *testing.T) {
	r := newRig(t)
	for _, ev := range []time.Time{at(0, 3, 0), at(0, 4, 0), at(0, 5, 0)} {
		r.adsb(ev, "") // positions without a callsign, as when the aircraft was out of ADS-B and heard by satellite
		r.msg(store.Message{T: at(2, 21, 24), Kind: "ops_report", Flight: "UA182", Callsign: "UAL182",
			Decoded: `{"fields":{"ops":{"event":"ER","origin":"KDEN","dest":"EDDF","at":"` + ev.Format(time.RFC3339) + `"}}}`})
	}
	segs := r.segments()
	if names(segs) != "UA182|" || segs[0].From != "KDEN" || segs[0].To != "EDDF" || segs[0].Start.After(at(0, 3, 30)) {
		t.Fatalf("segments: %s %+v", names(segs), segs)
	}
}

// an ADS-B position with a callsign, a place and an altitude, so that the flight can be judged airborne
func (r *rig) air(t time.Time, callsign string, lat, lon, alt float64, ground bool) {
	r.st.AddPosition(store.Position{Hex: hex, T: t, Lat: lat, Lon: lon, AltFt: &alt, OnGround: ground, Callsign: callsign, Source: "x"})
}

// a position carried by a message (ADS-C), with the flight number the aircraft gave
func (r *rig) adscAt(t time.Time, flight string, lat, lon, alt float64) {
	pt := t
	r.msg(store.Message{T: t, Kind: "adsc", Flight: flight, Lat: &lat, Lon: &lon, AltFt: &alt, PosT: &pt})
}

// VH-OQH's QF8 on 1 November: ADS-B saw it climb out over Mexico until 04:49Z, then nothing for 8 hours, then an ADS-C
// report from the South Pacific. Nearly 4,000 nm in 8.1 hours is 489 kt, the speed it had been flying: one flight.
func TestAirborneSilenceIsBridged(t *testing.T) {
	r := newRig(t)
	for i := 0; i <= 16; i++ { // 480 kt: 0.4 degrees of latitude every 3 minutes
		r.air(at(0, 4, 1).Add(time.Duration(i)*3*time.Minute), "QFA8", 25.19-0.4*float64(16-i), -104.93, 32000, false)
	}
	r.said(at(0, 12, 46), "QF8") // a weather request, with no position
	r.adscAt(at(0, 12, 55), "QF8", -10.25, -162.13, 38000)
	r.adscAt(at(0, 13, 5), "QF8", -11.2, -163.2, 38000)
	segs := r.segments()
	if len(segs) != 1 || segs[0].Flight != "QF8" || segs[0].Start.After(at(0, 4, 2)) || segs[0].End.Before(at(0, 13, 5)) {
		t.Fatalf("segments: %s %+v", names(segs), segs)
	}
}

// UPS81 stopped at Anchorage for hours: across the stop the average speed (about 310 kt) is a flight's, but the aircraft
// had been flying at 570 kt, and the pieces are two legs.
func TestAStopOnTheWayIsNotBridged(t *testing.T) {
	r := newRig(t)
	r.air(at(0, 18, 55), "UPS81", 55.13, 178.1, 31000, false)
	r.air(at(0, 19, 18), "UPS81", 56.72, -176.3, 31000, false) // 570 kt towards Anchorage
	r.air(at(1, 0, 9), "", 60.25, -148.25, 34800, false)       // leaving Anchorage, hours on (positions the number is not on)
	r.air(at(1, 4, 4), "", 34.62, -118.03, 20900, false)       // 2,700 nm and 8.8 hours from the last fix: 310 kt
	r.said(at(1, 3, 10), "UPS81")                              // the first mention of the number since 19:18: 7 hours 52 minutes
	if segs := r.segments(); len(segs) != 2 {
		t.Errorf("two legs, not one: %s %+v", names(segs), segs)
	}
}

// The same number a day later must not be joined to it: from the other end of the route, the speed needed would be
// far beyond a flight's (or, on a short route, far below).
func TestTheSameNumberTheNextDayIsNotBridged(t *testing.T) {
	r := newRig(t)
	// day one ends near Sydney; day two starts at Dallas eight hours later, both in the air
	for i := 0; i < 10; i++ {
		r.air(at(0, 8, 0).Add(time.Duration(i)*10*time.Minute), "QFA8", -30.0, 150.0, 35000, false)
	}
	for i := 0; i < 10; i++ {
		r.air(at(0, 18, 0).Add(time.Duration(i)*10*time.Minute), "QFA8", 32.9, -97.0, 20000, false)
	}
	if segs := r.segments(); len(segs) != 2 {
		t.Errorf("a turnaround is two flights: %s %+v", names(segs), segs)
	}
	// and a silence in which the second piece starts low (still near the ground) is not bridged either
	r2 := newRig(t)
	for i := 0; i < 10; i++ {
		r2.air(at(0, 1, 0).Add(time.Duration(i)*10*time.Minute), "QFA8", 25.0, -105.0, 32000, false)
	}
	for i := 0; i < 10; i++ {
		r2.air(at(0, 10, 0).Add(time.Duration(i)*10*time.Minute), "QFA8", -10.0, -162.0, 3000, false) // 489 kt, but not in the air up high
	}
	if segs := r2.segments(); len(segs) != 2 {
		t.Errorf("not high at the far end: %s", names(segs))
	}
}

// Qantas's cockpit asks for the weather along the next flight's route, and the airline sends that flight's data, while
// the aircraft is still flying this one (30 October: QF8 across the Pacific, QF63's requests all through it).
func TestNextFlightsRequestsDoNotInterleaveTheCurrentOne(t *testing.T) {
	r := newRig(t)
	spread(at(0, 0, 0), 6*time.Hour, 20*time.Minute, func(x time.Time) { r.adscAt(x, "QF8", 10, 100+float64(x.Sub(at(0, 0, 0))/time.Minute)/60, 38000) })
	weak := func(x time.Time) {
		r.msg(store.Message{T: x, Kind: "ops_report", Flight: "QF63", Callsign: "QFA63", Label: "5U"})
	}
	for _, m := range []int{70, 130, 190, 250, 310} { // bursts of them all through the flight: more than a stray
		for k := 0; k < 4; k++ {
			weak(at(0, 0, 0).Add(time.Duration(m+3*k) * time.Minute))
		}
	}
	if segs := r.segments(); len(segs) != 1 || segs[0].Flight != "QF8" {
		t.Fatalf("one flight, QF8: %s %+v", names(segs), segs)
	}
	// with nothing of the aircraft's own near them they are the evidence there is
	r2 := newRig(t)
	for _, m := range []int{0, 10, 20} {
		r2.msg(store.Message{T: at(1, 0, 0).Add(time.Duration(m) * time.Minute), Kind: "ops_report", Flight: "QF63", Callsign: "QFA63", Label: "5U"})
	}
	spread(at(1, 0, 0), 30*time.Minute, 10*time.Minute, func(x time.Time) { r2.adsb(x, "") })
	if segs := r2.segments(); len(segs) != 1 || segs[0].Flight != "QF63" {
		t.Errorf("weak evidence alone still names the flight: %s", names(segs))
	}
}

// A flight joined to the one before it by hand is one flight; taking the join away puts it back.
func TestJoinedByHand(t *testing.T) {
	r := newRig(t)
	spread(at(0, 2, 0), 2*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "CX110"); r.fix(x) })
	spread(at(0, 12, 0), 2*time.Hour, 20*time.Minute, func(x time.Time) { r.said(x, "CX111"); r.fix(x) }) // another number: two flights
	r.route(at(0, 3, 0), "VHHH", "EDDM")
	r.route(at(0, 13, 0), "EDDM", "KJFK")
	segs := r.segments()
	if len(segs) != 2 {
		t.Fatalf("before: %s", names(segs))
	}
	second := segs[1].Start
	if err := r.st.SetFlightJoin(hex, second, true, at(2, 0, 0)); err != nil {
		t.Fatal(err)
	}
	segs = r.segments()
	if len(segs) != 1 || segs[0].Flight != "CX110" || !segs[0].Start.Equal(at(0, 2, 0)) || !segs[0].End.Equal(at(0, 14, 0)) ||
		len(segs[0].Joined) != 1 || segs[0].Joined[0] != second.Unix() || segs[0].From != "VHHH" || segs[0].To != "KJFK" || !segs[0].Latest {
		t.Fatalf("joined: %s %+v", names(segs), segs)
	}
	if segs[0].Positions != 14 { // the 7 positions of each piece
		t.Errorf("positions %d", segs[0].Positions)
	}
	if err := r.st.SetFlightJoin(hex, second, false, at(2, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if segs = r.segments(); len(segs) != 2 {
		t.Errorf("after: %s", names(segs))
	}
	// a join made for the first flight has nothing before it to join to
	r.st.SetFlightJoin(hex, at(0, 2, 0), true, at(2, 0, 0))
	if segs = r.segments(); len(segs) != 2 {
		t.Errorf("nothing to join to: %s", names(segs))
	}
}

// The weak sightings at the ends of a flight are kept: the arrival traffic of a flight the aircraft hardly spoke on, and the
// next flight's data once the aircraft has landed. Only those inside another flight are set aside.
func TestWeakSightingsAtTheEndsOfAFlightAreKept(t *testing.T) {
	r := newRig(t)
	weak := func(x time.Time, flight string) {
		r.msg(store.Message{T: x, Kind: "ops_report", Flight: flight, Callsign: "QFA" + flight[2:], Label: "5U"})
	}
	// QF64 flies and lands at 03:50; its arrival traffic follows
	spread(at(0, 0, 0), 3*time.Hour+50*time.Minute, 10*time.Minute, func(x time.Time) { r.adscAt(x, "QF64", 10, 100+float64(x.Sub(at(0, 0, 0))/time.Minute)/60, 38000) })
	weak(at(0, 3, 55), "QF64")
	// the next flight's data after landing, more than a stray, then the aircraft's own sightings of it
	for _, m := range []int{70, 75, 80, 85} {
		weak(at(0, 3, 0).Add(time.Duration(m)*time.Minute), "QF1")
	}
	spread(at(0, 6, 0), time.Hour, 10*time.Minute, func(x time.Time) { r.adscAt(x, "QF1", 40, -100, 38000) })
	segs := r.segments()
	if names(segs) != "QF64|QF1|" || segs[1].Start.After(at(0, 4, 10)) {
		t.Errorf("QF1 starts with its data after QF64 lands: %s %+v", names(segs), segs)
	}
}

// An aircraft that has sat for hours sends its last callsign again when it powers up, and changes it a couple of seconds
// later (ZIPAIR's TZP1, then TZP22). That first sighting is not a late end to the flight before: it would stretch it over the
// whole time on the ground and into the next flight's climb.
func TestAStaleCallsignAtPowerUpIsTheNextFlights(t *testing.T) {
	r := newRig(t)
	// ZG1 arrives at 03:25, and is quiet
	spread(at(0, 2, 40), 45*time.Minute, time.Minute, func(x time.Time) { r.adsb(x, "TZP1") })
	// at 07:24:01 the first position still says TZP1; two seconds later TZP22, and it flies
	r.adsb(at(0, 7, 24).Add(time.Second), "TZP1")
	spread(at(0, 7, 24).Add(3*time.Second), 20*time.Minute, 10*time.Second, func(x time.Time) { r.adsb(x, "TZP22") })
	segs := r.segments()
	if names(segs) != "ZG1|ZG22|" && names(segs) != "TZP1|TZP22|" {
		t.Fatalf("%s", names(segs))
	}
	if !segs[0].End.Before(at(0, 3, 30)) || segs[1].Start.After(at(0, 7, 25)) || segs[1].Start.Before(at(0, 7, 23)) {
		t.Errorf("the old flight ends when it landed and the new one starts when it left: %+v %+v", segs[0], segs[1])
	}

	// the same callsign again after a long silence, with nothing following under another number, stays as it was
	r2 := newRig(t)
	spread(at(0, 2, 40), 45*time.Minute, time.Minute, func(x time.Time) { r2.adsb(x, "TZP1") })
	r2.adsb(at(0, 7, 24), "TZP1")
	if segs := r2.segments(); len(segs) != 1 || !segs[0].End.Equal(at(0, 7, 24)) {
		t.Errorf("a lone sighting after a silence is the flight's: %+v", segs)
	}
}

// an A350 report that names a route and the aircraft's own time for it, received at t (which may be long after)
func (r *rig) a350(t, happened time.Time, from, to string) {
	r.msg(store.Message{T: t, Kind: "miam", Decoded: `{"fields":{"a350":{"Kind":"REP216","Time":"` + happened.Format("2006-01-02T15:04:05Z") + `","Origin":"` + from + `","Dest":"` + to + `"}}}`})
}

// An A350 keeps its reports and delivers them late. A report from the flight before, delivered during this one, says where that
// flight went: it must not give this one its route (CX833 from Seattle showed Hong Kong to Munich, after CX301's route arrived
// two days late). A report from a moment with no flight of ours is still the flight it arrived in.
func TestLateReportsKeepToTheFlightTheyHappenedIn(t *testing.T) {
	r := newRig(t)
	// CX301, Hong Kong to Munich, flies 04:00-12:00; CX833, Seattle to Hong Kong, 20:00 to 06:00 the next day
	spread(at(0, 4, 0), 8*time.Hour, 10*time.Minute, func(x time.Time) { r.adscAt(x, "CX301", 30, 60, 38000) })
	spread(at(0, 20, 0), 10*time.Hour, 10*time.Minute, func(x time.Time) { r.adscAt(x, "CX833", 40, -150, 38000) })
	// this flight's own reports, on time
	r.a350(at(0, 21, 0), at(0, 21, 0), "KSEA", "VHHH")
	r.a350(at(1, 2, 0), at(1, 2, 0), "KSEA", "VHHH")
	// the other flight's, received during this one
	for _, m := range []int{0, 5, 10, 15} {
		r.a350(at(1, 3, m), at(0, 5, m), "VHHH", "EDDM")
	}
	segs := r.segments()
	if len(segs) != 2 || segs[0].From != "VHHH" || segs[0].To != "EDDM" || segs[1].From != "KSEA" || segs[1].To != "VHHH" {
		t.Errorf("each flight keeps its own route: %+v", segs)
	}

	// the same through a window that begins in the middle of the second flight, as the page's last four days do: the first flight
	// is before it, and still holds its own late report; the second is whole, with the same identity as in the full list
	narrow, err := Segments(r.st, hex, at(1, 1, 0), at(5, 0, 0))
	if err != nil || len(narrow) != 1 || narrow[0].ID != segs[1].ID || narrow[0].From != "KSEA" || narrow[0].To != "VHHH" || !narrow[0].Start.Equal(segs[1].Start) {
		t.Errorf("a window starting mid-flight: %+v %v", narrow, err)
	}

	// a flight whose reports all came late, with nothing of ours at the time they happened: they count where they arrived
	r2 := newRig(t)
	spread(at(1, 4, 0), 4*time.Hour, 10*time.Minute, func(x time.Time) { r2.adscAt(x, "SQ23", 40, -70, 38000) })
	r2.a350(at(1, 5, 0), at(0, 3, 0), "KJFK", "WSSS")
	if segs := r2.segments(); len(segs) != 1 || segs[0].From != "KJFK" || segs[0].To != "WSSS" {
		t.Errorf("a late report with no flight of ours where it happened: %+v", segs)
	}
}

func TestElsewhere(t *testing.T) {
	cx301 := Segment{ID: "CX301@1", Flight: "CX301", Identified: true, Start: at(0, 4, 0), End: at(0, 12, 0)}
	cx833 := Segment{ID: "CX833@2", Flight: "CX833", Identified: true, Start: at(0, 20, 0), End: at(1, 6, 0)}
	unknown := Segment{ID: "?@3", Start: at(2, 0, 0), End: at(2, 3, 0)}
	next301 := Segment{ID: "CX301@4", Flight: "CX301", Identified: true, Start: at(1, 20, 0), End: at(2, 4, 0)}
	segs := []Segment{cx301, cx833, unknown}
	for _, c := range []struct {
		name   string
		when   time.Time
		tagged string
		want   string // the flight it belongs to, or "" for where it is
	}{
		{"CX301's ATIS request arrived in the middle of CX833", at(1, 2, 19), "CX0301", "CX301@1"},
		{"the same, in the callsign form", at(1, 2, 19), "CPA301", "CX301@1"},
		{"CX833's own", at(1, 2, 19), "CX0833", ""},
		{"at the very end of CX301, still its own", at(0, 12, 10), "CX0301", ""},
		{"at the start of CX833, tagged with the flight before", at(0, 20, 10), "CX0301", "CX301@1"},
		{"between flights, in none", at(0, 15, 0), "CX0301", ""},
		{"tagged with a flight we have no record of", at(1, 2, 19), "CX0852", ""},
		{"in a flight whose number we never knew", at(2, 1, 0), "CX0301", ""},
		{"nonsense tag", at(1, 2, 19), "??", ""},
	} {
		o, ok := Elsewhere(segs, c.when, c.tagged)
		if got := map[bool]string{true: o.ID, false: ""}[ok]; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// the same number flies again the next evening: a message tagged with it may be about that flight, so it stays where it is
	if o, ok := Elsewhere(append([]Segment{cx301, cx833}, next301), at(1, 2, 19), "CX0301"); ok {
		t.Errorf("tagged with a number that flies again within a day and a half: moved to %s", o.ID)
	}
	if o, ok := Elsewhere(append([]Segment{cx301, cx833}, Segment{ID: "CX301@5", Flight: "CX301", Identified: true, Start: at(4, 20, 0), End: at(5, 4, 0)}), at(1, 2, 19), "CX0301"); !ok || o.ID != "CX301@1" {
		t.Errorf("a flight of that number four days on does not hold it back: %v %v", o.ID, ok)
	}
}

// QF1 flies Sydney to Singapore and on to London under one number. ADS-B and the messages saw it leave Sydney (reporting that route),
// and nothing of the flight to Singapore; then, ten hours later, ADS-B saw it climb out of Singapore. That climb-out shows a stop that
// no position recorded, and the flight is two: Sydney to Singapore, and from Singapore on.
func TestAStopADSBNeverSawSplitsTheFlight(t *testing.T) {
	// the first leg: a report of its route at 05:30, a fix near Sydney at 06:40, then only messages every two hours
	build := func(then func(r *rig)) []Segment {
		r := newRig(t)
		r.said(at(0, 5, 30), "QF1")
		r.route(at(0, 5, 30), "YSSY", "WSSS")
		r.air(at(0, 6, 40), "QFA1", -33.4, 150.5, 30000, false)
		for _, h := range []int{8, 10, 12, 14} {
			r.said(at(0, h, 0), "QF1")
		}
		then(r)
		return r.segments()
	}
	climbOut := func(r *rig) {
		for i, alt := range []float64{1575, 3150, 7000, 12000, 17000} { // up from Changi
			r.air(at(0, 16, 36).Add(time.Duration(i)*2*time.Minute), "QFA1", 1.40+float64(i)*0.1, 104.0-float64(i)*0.1, alt, false)
		}
	}
	segs := build(climbOut)
	if len(segs) != 2 || segs[0].Flight != "QF1" || segs[1].Flight != "QF1" {
		t.Fatalf("two legs of QF1: %s %+v", names(segs), segs)
	}
	a, b := segs[0], segs[1]
	if a.From != "YSSY" || a.To != "WSSS" || a.FromSeen || !a.End.Equal(at(0, 14, 0)) {
		t.Errorf("the leg to Singapore keeps its route and ends with its last message: %+v", a)
	}
	if b.From != "WSSS" || !b.FromSeen || b.To != "" || !b.Start.Equal(at(0, 16, 36)) || b.Positions != 5 {
		t.Errorf("the leg on from Singapore starts where it was seen leaving, from there: %+v", b)
	}

	for name, then := range map[string]func(r *rig){
		"a descent into Singapore, not a climb out of it": func(r *rig) {
			for i, alt := range []float64{5500, 4000, 2500, 1500, 800} { // low, and getting lower
				r.air(at(0, 16, 36).Add(time.Duration(i)*2*time.Minute), "QFA1", 1.40+float64(i)*0.1, 104.0-float64(i)*0.1, alt, false)
			}
		},
		"first seen at cruise height over Singapore, on a step climb": func(r *rig) {
			for i, alt := range []float64{33000, 34000, 36000} {
				r.air(at(0, 16, 36).Add(time.Duration(i)*2*time.Minute), "QFA1", 1.40+float64(i)*0.1, 104.0-float64(i)*0.1, alt, false)
			}
		},
		"a fix near Singapore before the climb-out: it may only have circled": func(r *rig) {
			r.air(at(0, 13, 0), "QFA1", 1.5, 104.2, 3000, false)
			climbOut(r)
		},
		"a climb with too little time to have got there": func(r *rig) {
			// Sydney at 06:40 to Singapore at 450 kt is about 7.5 hours (to 14:13): a climb-out at 14:40 leaves no stop worth the name
			for i, alt := range []float64{1575, 3150, 7000, 12000} {
				r.air(at(0, 14, 40).Add(time.Duration(i)*2*time.Minute), "QFA1", 1.40+float64(i)*0.1, 104.0-float64(i)*0.1, alt, false)
			}
		},
	} {
		if segs := build(then); len(segs) != 1 {
			t.Errorf("%s: %d flights, want one: %+v", name, len(segs), segs)
		}
	}

	// a fix near Jakarta at 14:00 and the climb-out of Singapore at 16:36: time enough for a stop, but a silence of under three hours is
	// not long enough to say it was one
	r := newRig(t)
	r.said(at(0, 5, 30), "QF1")
	r.route(at(0, 5, 30), "YSSY", "WSSS")
	r.air(at(0, 14, 0), "QFA1", -6.0, 107.0, 30000, false)
	for i, alt := range []float64{1575, 3150, 7000, 12000} {
		r.air(at(0, 16, 36).Add(time.Duration(i)*2*time.Minute), "QFA1", 1.40+float64(i)*0.1, 104.0-float64(i)*0.1, alt, false)
	}
	if segs := r.segments(); len(segs) != 1 {
		t.Errorf("a silence of under three hours: %d flights", len(segs))
	}
}

func TestAnOriginSeenOnADSBGivesWayToOneEnteredByHand(t *testing.T) {
	sg := Segment{From: "WSSS", FromSeen: true, Start: at(0, 16, 0), End: at(0, 22, 0)}
	applyManualRoute(&sg, []store.ManualRoute{{From: "VHHH", To: "EGLL", Start: at(0, 15, 0), End: at(0, 23, 0), Created: at(1, 0, 0)}})
	if sg.From != "VHHH" || !sg.FromManual || sg.FromSeen || sg.To != "EGLL" {
		t.Errorf("%+v", sg)
	}
	sg = Segment{From: "WSSS", FromSeen: true, Start: at(0, 16, 0), End: at(0, 22, 0)}
	applyManualRoute(&sg, []store.ManualRoute{{To: "EGLL", Start: at(0, 15, 0), End: at(0, 23, 0), Created: at(1, 0, 0)}})
	if sg.From != "WSSS" || !sg.FromSeen || sg.To != "EGLL" {
		t.Errorf("a destination alone leaves the seen origin: %+v", sg)
	}
}
