package waypoints

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/geo"
	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/store"
)

const hex = "780AA4"

var t0 = time.Date(2026, 10, 30, 18, 48, 0, 0, time.UTC) // the position time of the CPDLC report under test

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fp(v float64) *float64 { return &v }

// cpdlc stores a position report at (lat, lon), t0, flying track 122 at 520 kt, naming the given fixes.
func cpdlc(t *testing.T, st *store.Store, id int64, lat, lon float64, entries ...store.FixEntry) store.Message {
	t.Helper()
	fl, _ := json.Marshal(store.FixList{GS: fp(520), Trk: fp(122), List: entries})
	pt := t0
	m := store.Message{ID: id, Hex: hex, T: t0.Add(-3 * time.Minute), Label: "BA", Text: "x" + string(rune('a'+id)), Kind: "cpdlc",
		Lat: &lat, Lon: &lon, PosT: &pt, Fixes: string(fl)}
	if _, err := st.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func at(h, m int) int64 { return time.Date(2026, 10, 30, h, m, 0, 0, time.UTC).Unix() }

func find(ws []Waypoint, name string) *Waypoint {
	for i := range ws {
		if ws[i].Name == name {
			return &ws[i]
		}
	}
	return nil
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v ± %v", what, got, want, tol)
	}
}

func TestAdvance(t *testing.T) {
	lat, lon := Advance(0, 100, 0, 60) // 60 nm north
	near(t, "north lat", lat, 1, 1e-6)
	near(t, "north lon", lon, 100, 1e-6)
	lat, lon = Advance(0, 100, 90, 60) // 60 nm east on the equator
	near(t, "east lat", lat, 0, 1e-6)
	near(t, "east lon", lon, 101, 1e-6)
	_, lon = Advance(60, 0, 90, 60) // at 60 degrees a degree of longitude is half as far
	near(t, "high-latitude east", lon, 2, 0.02)
}

// The case that motivated all of this: B-LRD at 5.46S 141.74E on track 122, its report naming
// OLGAN (passed 18:26), KADAB (next, ETA 18:55) and KAPPU (then). The 2013 database puts KADAB 44 nm
// BEHIND the aircraft, and KAPPU nearer than the "next" fix, so neither may be believed.
func TestStaleDatabaseIsNotBelieved(t *testing.T) {
	st := open(t)
	// ADS-B/message fixes either side of 18:26, for the passed fix to be interpolated between.
	st.AddPosition(store.Position{Hex: hex, T: time.Date(2026, 10, 30, 18, 20, 0, 0, time.UTC), Lat: -3.4, Lon: 138.9, Source: "x"})
	st.AddPosition(store.Position{Hex: hex, T: time.Date(2026, 10, 30, 18, 32, 0, 0, time.UTC), Lat: -4.4, Lon: 140.1, Source: "x"})
	m := cpdlc(t, st, 1, -5.46, 141.742,
		store.FixEntry{Name: "OLGAN", Role: "passed", T: at(18, 26)},
		store.FixEntry{Name: "KADAB", Role: "next", T: at(18, 55)},
		store.FixEntry{Name: "KAPPU", Role: "then"})
	nav := navdata.New()
	nav.Add("KADAB", navdata.Point{Lat: -4.965, Lon: 141.0, Kind: "fix"})
	nav.Add("KAPPU", navdata.Point{Lat: -5.885, Lon: 142.21, Kind: "fix"})
	ws := (&Resolver{Store: st, Nav: nav}).Resolve(m)

	k := find(ws, "KADAB")
	if k == nil || k.Exact || k.Source != "projected" {
		t.Fatalf("KADAB should be a projected estimate, got %+v", k)
	}
	// 7 minutes at 520 kt on track 122 is 60.7 nm: about 31.9 nm south and 51.5 nm east.
	near(t, "KADAB lat", k.Lat, -5.46-31.9/60, 0.05)
	near(t, "KADAB lon", k.Lon, 141.742+51.5/60/math.Cos(5.7*math.Pi/180), 0.05)
	if find(ws, "KAPPU") != nil {
		t.Errorf("KAPPU (nearer than the next fix) was placed: %+v", find(ws, "KAPPU"))
	}
	o := find(ws, "OLGAN")
	if o == nil || o.Exact || o.Source != "interpolated" {
		t.Fatalf("OLGAN = %+v", o)
	}
	// 18:26 is half way between 18:20 and 18:32.
	near(t, "OLGAN lat", o.Lat, -3.9, 1e-6)
	near(t, "OLGAN lon", o.Lon, 139.5, 1e-6)
}

func TestConsistentDatabasePositionIsUsed(t *testing.T) {
	st := open(t)
	m := cpdlc(t, st, 1, -5.46, 141.742, store.FixEntry{Name: "NEXTX", Role: "next", T: at(18, 55)}, store.FixEntry{Name: "THENX", Role: "then"})
	nav := navdata.New()
	nav.Add("NEXTX", navdata.Point{Lat: -6.0, Lon: 142.7, Kind: "fix"}) // ~6 nm from the projection
	nav.Add("THENX", navdata.Point{Lat: -6.8, Lon: 144.2, Kind: "fix"}) // further on, ahead
	ws := (&Resolver{Store: st, Nav: nav}).Resolve(m)
	n, th := find(ws, "NEXTX"), find(ws, "THENX")
	if n == nil || !n.Exact || n.Source != "database" || n.Lat != -6.0 {
		t.Errorf("NEXTX = %+v", n)
	}
	if th == nil || !th.Exact || th.Source != "database" {
		t.Errorf("THENX = %+v", th)
	}
}

func TestDatabaseDuplicatesChooseTheNearOne(t *testing.T) {
	st := open(t)
	m := cpdlc(t, st, 1, -5.46, 141.742, store.FixEntry{Name: "DUP", Role: "then"})
	nav := navdata.New()
	nav.Add("DUP", navdata.Point{Lat: 40, Lon: -100, Kind: "fix"})    // same name, other side of the world
	nav.Add("DUP", navdata.Point{Lat: -6.5, Lon: 143.0, Kind: "fix"}) // ahead of the aircraft
	if d := find((&Resolver{Store: st, Nav: nav}).Resolve(m), "DUP"); d == nil || d.Lat != -6.5 {
		t.Errorf("DUP = %+v", d)
	}
	// A name that exists only far away is not placed at all.
	m = cpdlc(t, st, 2, -5.46, 141.742, store.FixEntry{Name: "FARAWAY", Role: "then"})
	nav2 := navdata.New()
	nav2.Add("FARAWAY", navdata.Point{Lat: 40, Lon: -100, Kind: "fix"})
	if got := (&Resolver{Store: st, Nav: nav2}).Resolve(m); len(got) != 0 {
		t.Errorf("placed %+v", got)
	}
}

func TestLearnedBeatsDatabaseAndMustAgreeWithTheFlight(t *testing.T) {
	st := open(t)
	m := cpdlc(t, st, 1, -5.46, 141.742, store.FixEntry{Name: "NEXTX", Role: "next", T: at(18, 55)}, store.FixEntry{Name: "THENX", Role: "then"})
	nav := navdata.New()
	nav.Add("NEXTX", navdata.Point{Lat: -6.0, Lon: 142.7, Kind: "fix"})
	nav.Add("THENX", navdata.Point{Lat: -6.8, Lon: 144.2, Kind: "fix"})
	// A learned position slightly different from the database one wins, and is marked as from ADS-C.
	st.AddFix("NEXTX", 99, -6.02, 142.68, "adsc-eta")
	// A learned position for THENX that is nowhere near anything plausible is still taken when it
	// is within reach (there is no estimate to contradict), but not when it is on the other side of the world.
	st.AddFix("THENX", 98, 50, 10, "adsc-eta")
	ws := (&Resolver{Store: st, Nav: nav}).Resolve(m)
	if n := find(ws, "NEXTX"); n == nil || n.Source != "ADS-C" || n.Lat != -6.02 || !n.Exact {
		t.Errorf("NEXTX = %+v", n)
	}
	if th := find(ws, "THENX"); th == nil || th.Source != "database" {
		t.Errorf("THENX should fall through the unreachable learned fix to the database: %+v", th)
	}
	// A learned position that contradicts the estimate for a fix with a time (a reused name) is rejected.
	st2 := open(t)
	m2 := cpdlc(t, st2, 1, -5.46, 141.742, store.FixEntry{Name: "REUSED", Role: "next", T: at(18, 55)})
	st2.AddFix("REUSED", 97, -2.0, 137.0, "adsc-eta") // 400+ nm from where it must be
	if w := find((&Resolver{Store: st2}).Resolve(m2), "REUSED"); w == nil || w.Exact || w.Source != "projected" {
		t.Errorf("REUSED = %+v", w)
	}
}

func TestResolveNeedsAReferencePosition(t *testing.T) {
	st := open(t)
	m := cpdlc(t, st, 1, -5.46, 141.742, store.FixEntry{Name: "A", Role: "next", T: at(18, 55)})
	m.Lat = nil
	if got := (&Resolver{Store: st}).Resolve(m); got != nil {
		t.Errorf("placed without a position: %+v", got)
	}
	if got := (&Resolver{Store: st}).Resolve(store.Message{}); got != nil {
		t.Errorf("placed from nothing: %+v", got)
	}
}

func TestNextFixAlreadyDueIsNotProjected(t *testing.T) {
	st := open(t)
	m := cpdlc(t, st, 1, -5.46, 141.742, store.FixEntry{Name: "LATE", Role: "next", T: at(18, 40)}) // ETA before the report
	if got := (&Resolver{Store: st}).Resolve(m); len(got) != 0 {
		t.Errorf("projected backwards: %+v", got)
	}
	m = cpdlc(t, st, 2, -5.46, 141.742, store.FixEntry{Name: "TOOFAR", Role: "next", T: at(21, 0)}) // 2h12m away: 1,100 nm
	if got := (&Resolver{Store: st}).Resolve(m); len(got) != 0 {
		t.Errorf("projected %+v", got)
	}
}

func TestLearn(t *testing.T) {
	st := open(t)
	adsc := func(id int64, pos time.Time, etaSec float64, lat, lon float64) {
		r, _ := json.Marshal([]map[string]any{{"Lat": lat, "Lon": lon, "ETASec": etaSec}, {"Lat": 1, "Lon": 1}})
		la, lo := -4.5, 140.2
		st.AddMessage(store.Message{ID: id, Hex: hex, T: pos, Label: "B6", Text: "a" + string(rune('a'+id)), Kind: "adsc", Lat: &la, Lon: &lo, PosT: &pos, Route: string(r)})
	}
	// An ADS-C report at 18:34:31 whose next waypoint, at (-5.233, 140.798), is due in 443 s: 18:41:54.
	adsc(1, time.Date(2026, 10, 30, 18, 34, 31, 0, time.UTC), 443, -5.233, 140.798)
	// A CPDLC report names: a fix passed at 18:42 (12 s off), a next fix due 18:55 (no ADS-C waypoint then), and one after.
	cpdlc(t, st, 2, -5.46, 141.742,
		store.FixEntry{Name: "MATCHED", Role: "passed", T: at(18, 42)},
		store.FixEntry{Name: "NOMATCH", Role: "next", T: at(18, 55)},
		store.FixEntry{Name: "AFTER", Role: "then"})
	n, err := Learn(st, hex, t0.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("learned %d, err %v", n, err)
	}
	if l := st.LearnedFixes("MATCHED"); len(l) != 1 || l[0].Lat != -5.233 || l[0].Lon != 140.798 || l[0].N != 1 {
		t.Errorf("MATCHED = %+v", l)
	}
	for _, name := range []string{"NOMATCH", "AFTER"} {
		if l := st.LearnedFixes(name); l != nil {
			t.Errorf("%s learned %+v", name, l)
		}
	}
	// Learning again over the same messages is a no-op.
	if n, _ = Learn(st, hex, t0.Add(-24*time.Hour)); n != 1 || st.LearnedFixes("MATCHED")[0].N != 1 {
		t.Errorf("second pass: n=%d fixes=%+v", n, st.LearnedFixes("MATCHED"))
	}
	// A second report that names it agrees, so the position has two supporting messages.
	cpdlc(t, st, 3, -5.0, 141.2, store.FixEntry{Name: "MATCHED", Role: "passed", T: at(18, 41)})
	Learn(st, hex, t0.Add(-24*time.Hour))
	if l := st.LearnedFixes("MATCHED"); len(l) != 1 || l[0].N != 2 {
		t.Errorf("after a second message: %+v", l)
	}
	// And the match makes the resolver place it exactly.
	got := find((&Resolver{Store: st}).Resolve(cpdlcMsg(st, 2)), "MATCHED")
	if got == nil || !got.Exact || got.Source != "ADS-C" || math.Abs(geo.DistNM(got.Lat, got.Lon, -5.233, 140.798)) > 0.01 {
		t.Errorf("resolved = %+v", got)
	}
}

func cpdlcMsg(st *store.Store, id int64) store.Message {
	ms, _ := st.Messages(hex, time.Time{}, time.Now().Add(48*time.Hour), 100)
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	return store.Message{}
}

// Near the date line longitudes jump from 180 to -180; nothing here may treat that as a 360 degree leap.
func TestAcrossTheDateLine(t *testing.T) {
	// Advance 60 nm east from 179.5E on the equator lands at 179.5 + 1 = 180.5, i.e. 179.5W.
	_, lon := Advance(0, 179.5, 90, 60)
	near(t, "advance east across 180", lon, -179.5, 1e-6)
	_, lon = Advance(0, -179.5, 270, 60)
	near(t, "advance west across -180", lon, 179.5, 1e-6)
	// Bearings and the ahead/behind decision across it.
	near(t, "bearing east across 180", bearing(0, 179.5, 0, -179.5), 90, 0.01)
	near(t, "bearing west across 180", bearing(0, -179.5, 0, 179.5), 270, 0.01)

	// A fix passed at 12:00, half way between ADS-B points at 179.0E and 179.0W (2 degrees apart)
	// must be interpolated to the date line, not to 0 on the far side of the world.
	st := open(t)
	base := time.Date(2026, 10, 30, 11, 50, 0, 0, time.UTC)
	st.AddPosition(store.Position{Hex: hex, T: base, Lat: 10, Lon: 179.0, Source: "x"})
	st.AddPosition(store.Position{Hex: hex, T: base.Add(20 * time.Minute), Lat: 10, Lon: -179.0, Source: "x"})
	lat, lon0 := 10.0, -179.0
	pt := base.Add(20 * time.Minute)
	fl, _ := json.Marshal(store.FixList{GS: fp(500), Trk: fp(90), List: []store.FixEntry{{Name: "DLINE", Role: "passed", T: base.Add(10 * time.Minute).Unix()}}})
	m := store.Message{ID: 1, Hex: hex, T: pt, Label: "BA", Text: "x", Kind: "cpdlc", Lat: &lat, Lon: &lon0, PosT: &pt, Fixes: string(fl)}
	ws := (&Resolver{Store: st}).Resolve(m)
	d := find(ws, "DLINE")
	if d == nil || math.Abs(geo.NormLon(d.Lon)) < 179.9 {
		t.Fatalf("interpolated across the date line to %+v", d)
	}
	near(t, "distance to the date line", math.Abs(geo.DLon(d.Lon, 180)), 0, 0.01)

	// A next fix projected eastward across the date line comes out as a valid longitude.
	m2 := cpdlc(t, st, 2, 10, 179.5, store.FixEntry{Name: "NXT", Role: "next", T: t0.Add(7 * time.Minute).Unix()})
	nx := find((&Resolver{Store: st}).Resolve(m2), "NXT")
	if nx == nil || nx.Lon < -180 || nx.Lon > 180 {
		t.Errorf("projected fix longitude %+v", nx)
	}
}

// adscRoute stores an ADS-C report at (lat, lon), pos, with a predicted route and optionally a
// waypoint change event at that position.
func adscRoute(t *testing.T, st *store.Store, id int64, pos time.Time, lat, lon float64, event bool, route ...map[string]any) {
	t.Helper()
	r, _ := json.Marshal(route)
	decoded := `{"tree":null}`
	if event {
		decoded = `{"tree":{"arinc622":{"adsc":{"tags":[{"wpt_change_event":{"lat":` + strconv.FormatFloat(lat, 'f', 6, 64) + `,"lon":` + strconv.FormatFloat(lon, 'f', 6, 64) + `,"ts_sec":1}}]}}}}`
	}
	m := store.Message{ID: id, Hex: hex, T: pos, Label: "B6", Text: "adsc" + strconv.Itoa(int(id)), Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &pos, Route: string(r), Decoded: decoded}
	if _, err := st.AddMessage(m); err != nil {
		t.Fatal(err)
	}
}

func wp(lat, lon float64, eta float64, kind string) map[string]any {
	m := map[string]any{"Lat": lat, "Lon": lon, "Kind": kind}
	if eta > 0 {
		m["ETASec"] = eta
	}
	return m
}

func learned(t *testing.T, st *store.Store, name string, lat, lon float64) {
	t.Helper()
	l := st.LearnedFixes(name)
	if len(l) != 1 || math.Abs(l[0].Lat-lat) > 1e-6 || math.Abs(l[0].Lon-lon) > 1e-6 {
		t.Errorf("%s learned %+v, want %v,%v", name, l, lat, lon)
	}
}

// The waypoint after a matched one in a CPDLC list is the next one in the same ADS-C report.
func TestLearnFollowsTheRoute(t *testing.T) {
	st := open(t)
	// ADS-C at 18:34:31: next (-5.233, 140.798) due in 443 s (18:41:54), after it (-5.9, 141.5); then the projection.
	adscRoute(t, st, 1, time.Date(2026, 10, 30, 18, 34, 31, 0, time.UTC), -4.5, 140.2, false,
		wp(-5.233, 140.798, 443, "next"), wp(-5.9, 141.5, 0, "next+1"), wp(-9, 144, 900, "fixed"))
	// Passed at 18:42 = the ADS-C next waypoint, so the CPDLC next fix is the one after it. The fix
	// after next is not in the ADS-C report, and the fixed-time projection is not a waypoint.
	cpdlc(t, st, 2, -5.46, 141.742,
		store.FixEntry{Name: "PASSED", Role: "passed", T: at(18, 42)},
		store.FixEntry{Name: "NEXT", Role: "next", T: at(18, 55)},
		store.FixEntry{Name: "THEN", Role: "then"})
	if n, err := Learn(st, hex, t0.Add(-24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("learned %d, err %v", n, err)
	}
	learned(t, st, "PASSED", -5.233, 140.798)
	learned(t, st, "NEXT", -5.9, 141.5)
	if l := st.LearnedFixes("THEN"); l != nil {
		t.Errorf("THEN learned %+v", l)
	}
	// The same pass again changes nothing.
	if n, _ := Learn(st, hex, t0.Add(-24*time.Hour)); n != 2 || st.LearnedFixes("NEXT")[0].N != 1 {
		t.Errorf("second pass: n=%d", n)
	}
	// A route that cannot be this flight's (the other side of the world) is not believed.
	st2 := open(t)
	adscRoute(t, st2, 1, time.Date(2026, 10, 30, 18, 34, 31, 0, time.UTC), -4.5, 140.2, false,
		wp(-5.233, 140.798, 443, "next"), wp(50, 10, 0, "next+1"))
	cpdlc(t, st2, 2, -5.46, 141.742, store.FixEntry{Name: "PASSED", Role: "passed", T: at(18, 42)}, store.FixEntry{Name: "NEXT", Role: "next", T: at(18, 55)})
	Learn(st2, hex, t0.Add(-24*time.Hour))
	if l := st2.LearnedFixes("NEXT"); l != nil {
		t.Errorf("NEXT learned %+v", l)
	}
}

// A waypoint change event is the aircraft sequencing a waypoint; the route in the same report is what follows.
func TestLearnFromWaypointChangeEvent(t *testing.T) {
	st := open(t)
	adscRoute(t, st, 1, time.Date(2026, 10, 30, 18, 42, 20, 0, time.UTC), -5.30, 140.85, true,
		wp(-5.9, 141.5, 300, "next"), wp(-6.4, 142.1, 0, "next+1"))
	cpdlc(t, st, 2, -5.46, 141.742,
		store.FixEntry{Name: "PASSED", Role: "passed", T: at(18, 42)},
		store.FixEntry{Name: "NEXT", Role: "next", T: at(18, 55)},
		store.FixEntry{Name: "THEN", Role: "then"})
	if n, err := Learn(st, hex, t0.Add(-24*time.Hour)); err != nil || n != 3 {
		t.Fatalf("learned %d, err %v", n, err)
	}
	learned(t, st, "PASSED", -5.30, 140.85)
	learned(t, st, "NEXT", -5.9, 141.5)
	learned(t, st, "THEN", -6.4, 142.1)

	// An earlier prediction of that waypoint, with an ETA that matches, is the better position: the
	// event is where the aircraft sequenced it, a few miles off.
	adscRoute(t, st, 3, time.Date(2026, 10, 30, 18, 35, 0, 0, time.UTC), -4.5, 140.2, false, wp(-5.233, 140.798, 420, "next"))
	Learn(st, hex, t0.Add(-24*time.Hour))
	if l := st.LearnedFixes("PASSED"); len(l) != 1 || l[0].Lat != -5.233 {
		t.Errorf("PASSED after an ETA match = %+v", l)
	}
}
