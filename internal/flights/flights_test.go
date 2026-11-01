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
