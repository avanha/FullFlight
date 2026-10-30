package poller

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

func TestCallsignCandidates(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"UA869", []string{"UAL869", "UAL0869"}, false},
		{"CX833", []string{"CPA833", "CPA0833"}, false},
		{"UAL869", []string{"UAL869", "UAL0869"}, false},
		{"BA1234", []string{"BAW1234"}, false},
		{"ZZ12", nil, true},
		{"nonsense!", nil, true},
	}
	for _, c := range cases {
		got, err := CallsignCandidates(c.in)
		if (err != nil) != c.err || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, %v", c.in, got, err)
		}
	}
}

type fake struct {
	byCallsign map[string][]adsb.Aircraft
	byHex      map[string][]adsb.Aircraft
	byReg      map[string][]adsb.Aircraft
	calls      []string
}

func (f *fake) Hex(_ context.Context, h string) ([]adsb.Aircraft, error) {
	f.calls = append(f.calls, "hex:"+h)
	return f.byHex[h], nil
}
func (f *fake) Callsign(_ context.Context, c string) ([]adsb.Aircraft, error) {
	f.calls = append(f.calls, "cs:"+c)
	return f.byCallsign[c], nil
}
func (f *fake) Reg(_ context.Context, r string) ([]adsb.Aircraft, error) {
	f.calls = append(f.calls, "reg:"+r)
	return f.byReg[r], nil
}

func fl(v float64) *float64 { return &v }

func ac(lat float64) adsb.Aircraft {
	alt := 35000.0
	return adsb.Aircraft{Hex: "A1B2C3", Flight: "UAL869  ", Reg: "N12345", Lat: fl(lat), Lon: fl(-100),
		AltBaro: adsb.AltBaro{Feet: &alt}, SeenPos: fl(2), Source: "adsb.lol"}
}

func TestFlightLatches(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	e, err := watchlist.Parse("UA869 2026-10-31", now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	st.AddWatch(e)

	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	p := &Poller{Store: st, ADSB: f, Now: func() time.Time { return now }}

	// Not airborne yet: both callsign variants tried, entry stays waiting.
	p.Once(context.Background())
	if !reflect.DeepEqual(f.calls, []string{"cs:UAL869", "cs:UAL0869"}) {
		t.Fatalf("calls = %v", f.calls)
	}
	if w, _ := st.ListWatches(true); w[0].Status != watchlist.StatusWaiting || w[0].Hex != "" {
		t.Fatalf("entry = %+v", w[0])
	}

	// Waiting entries are not re-polled within WaitingEvery.
	f.calls = nil
	now = now.Add(time.Minute)
	p.Once(context.Background())
	if len(f.calls) != 0 {
		t.Errorf("polled too soon: %v", f.calls)
	}

	// It takes off: the entry latches onto the hex.
	f.byCallsign["UAL869"] = []adsb.Aircraft{ac(40)}
	f.byHex["A1B2C3"] = []adsb.Aircraft{ac(40.5)}
	now = now.Add(5 * time.Minute)
	p.Once(context.Background())
	w, _ := st.ListWatches(true)
	if w[0].Status != watchlist.StatusTracking || w[0].Hex != "A1B2C3" || w[0].Tail != "N12345" || w[0].Callsign != "UAL869" {
		t.Fatalf("entry = %+v", w[0])
	}

	// From then on it follows the hex, not the callsign.
	f.calls = nil
	now = now.Add(time.Minute)
	p.Once(context.Background())
	if !reflect.DeepEqual(f.calls, []string{"hex:A1B2C3"}) {
		t.Errorf("calls = %v", f.calls)
	}
	tr, _ := st.Track("A1B2C3", now.Add(-time.Hour), now.Add(time.Hour))
	if len(tr) != 2 || tr[0].Lat != 40 || tr[1].Lat != 40.5 || tr[0].Source != "adsb.lol" {
		t.Errorf("track = %+v", tr)
	}
}

func TestTailTracksImmediately(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("N12345", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byReg: map[string][]adsb.Aircraft{"N12345": {ac(41)}}}
	p := &Poller{Store: st, ADSB: f, Now: func() time.Time { return now }}
	p.Once(context.Background())
	w, _ := st.ListWatches(true)
	if w[0].Hex != "A1B2C3" || w[0].Callsign != "UAL869" {
		t.Errorf("entry = %+v", w[0])
	}
	// Past its end it is no longer polled and gets marked done.
	f.calls = nil
	now = now.Add(37 * time.Hour)
	p.Once(context.Background())
	if len(f.calls) != 0 {
		t.Errorf("polled after end: %v", f.calls)
	}
	if w, _ := st.ListWatches(false); w[0].Status != watchlist.StatusDone {
		t.Errorf("status = %v", w[0].Status)
	}
}

func flying(lat float64, cs string) adsb.Aircraft {
	a := ac(lat)
	a.Flight = cs
	return a
}

func parked(cs string) adsb.Aircraft {
	a := flying(40, cs)
	a.AltBaro = adsb.AltBaro{OnGround: true}
	a.GS = fl(0)
	return a
}

// flightRig runs a flight entry through a scripted sequence of sightings.
type flightRig struct {
	t   *testing.T
	st  *store.Store
	f   *fake
	p   *Poller
	now time.Time
}

func newFlightRig(t *testing.T) *flightRig {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &flightRig{t: t, st: st, f: &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}},
		now: time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)}
	e, _ := watchlist.Parse("UA869 2026-10-31", r.now, time.Time{})
	st.AddWatch(e)
	r.p = &Poller{Store: st, ADSB: r.f, Now: func() time.Time { return r.now }, WaitingEvery: time.Second, TrackingEvery: time.Second}
	return r
}

// step advances the clock, sets what the feed shows (nil = nothing), and polls.
func (r *flightRig) step(d time.Duration, seen *adsb.Aircraft) watchlist.Entry {
	r.now = r.now.Add(d)
	r.f.byCallsign["UAL869"], r.f.byHex["A1B2C3"] = nil, nil
	if seen != nil {
		r.f.byCallsign["UAL869"] = []adsb.Aircraft{*seen}
		r.f.byHex["A1B2C3"] = []adsb.Aircraft{*seen}
	}
	r.p.Once(context.Background())
	all, _ := r.st.ListWatches(false)
	return all[0]
}

func TestStopoverKeepsWatching(t *testing.T) {
	r := newFlightRig(t)
	g := parked("UAL869")
	if e := r.step(0, &g); e.Status != watchlist.StatusTracking || e.Airborne {
		t.Fatalf("on the ramp before departure: %+v", e) // not "landed": it has not flown yet
	}
	up := flying(40, "UAL869")
	if e := r.step(time.Minute, &up); !e.Airborne {
		t.Fatalf("airborne not recorded: %+v", e)
	}
	// Lands at the intermediate stop, sits for 90 minutes (< 3 h grace).
	if e := r.step(time.Hour, &g); e.LandedAt.IsZero() || e.Status != watchlist.StatusTracking {
		t.Fatalf("landing not recorded: %+v", e)
	}
	if e := r.step(90*time.Minute, &g); e.Status != watchlist.StatusTracking {
		t.Fatalf("ended during a stopover: %+v", e)
	}
	// Even a few minutes off the feed (transponder off at the gate) is fine.
	if e := r.step(10*time.Minute, nil); e.Status != watchlist.StatusTracking {
		t.Fatalf("ended while off the feed: %+v", e)
	}
	// Departs again: still the same flight, landing cleared.
	up2 := flying(41, "UAL869")
	if e := r.step(10*time.Minute, &up2); !e.LandedAt.IsZero() || e.Status != watchlist.StatusTracking {
		t.Fatalf("second leg: %+v", e)
	}
	// Final landing; after the grace it is done, even though the aircraft vanished from the feed.
	if e := r.step(2*time.Hour, &g); e.LandedAt.IsZero() {
		t.Fatalf("final landing: %+v", e)
	}
	if e := r.step(2*time.Hour, nil); e.Status != watchlist.StatusTracking {
		t.Fatalf("done too early (2h < 3h): %+v", e)
	}
	if e := r.step(90*time.Minute, nil); e.Status != watchlist.StatusDone {
		t.Fatalf("not done after grace: %+v", e)
	}
}

func TestCallsignChangeAfterLandingEndsFlight(t *testing.T) {
	r := newFlightRig(t)
	up := flying(40, "UAL869")
	r.step(0, &up)
	g := parked("UAL869")
	r.step(time.Hour, &g)
	next := parked("UAL1234") // same airframe, new flight number, 20 minutes later
	e := r.step(20*time.Minute, &next)
	if e.Status != watchlist.StatusDone || e.Callsign != "UAL869" {
		t.Fatalf("entry = %+v", e)
	}
	// And nothing from the next flight is recorded against this watch.
	tr, _ := r.st.Track("A1B2C3", r.now.Add(-24*time.Hour), r.now.Add(time.Hour))
	for _, p := range tr {
		if p.Callsign == "UAL1234" {
			t.Errorf("recorded the next flight: %+v", p)
		}
	}
}

func TestCallsignChangeInFlightIsIgnored(t *testing.T) {
	r := newFlightRig(t)
	up := flying(40, "UAL869")
	r.step(0, &up)
	odd := flying(41, "UAL869X")
	if e := r.step(time.Minute, &odd); e.Status != watchlist.StatusTracking {
		t.Errorf("a mid-flight callsign blip ended the watch: %+v", e)
	}
}

type fakeLookup struct {
	hex   string
	calls int
}

func (l *fakeLookup) ByTail(_ context.Context, tail string) (*airframes.Airframe, error) {
	l.calls++
	if l.hex == "" {
		return nil, nil
	}
	return &airframes.Airframe{Tail: tail, ICAO: l.hex, Type: "A359"}, nil
}

func TestTailResolvedBeforeItFlies(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byHex: map[string][]adsb.Aircraft{}, byReg: map[string][]adsb.Aircraft{}}
	lk := &fakeLookup{hex: "780AB8"}
	p := &Poller{Store: st, ADSB: f, Airframes: lk, Now: func() time.Time { return now }, TrackingEvery: time.Second}

	p.Once(context.Background())
	if !reflect.DeepEqual(f.calls, []string{"hex:780AB8"}) {
		t.Fatalf("expected a hex query, got %v", f.calls)
	}
	if w, _ := st.ListWatches(true); w[0].Hex != "780AB8" || w[0].Tail != "B-LRM" {
		t.Errorf("entry = %+v", w[0])
	}
	// The pairing outlives the watch and needs no second lookup.
	if hex, ok := st.HexForTail("B-LRM"); !ok || hex != "780AB8" {
		t.Errorf("table: %q %v", hex, ok)
	}
	st.DeleteWatch(1)
	e2, _ := watchlist.Parse("B-LRM", now.Add(time.Hour), time.Time{})
	st.AddWatch(e2)
	now = now.Add(time.Hour)
	p.Once(context.Background())
	if lk.calls != 1 {
		t.Errorf("airframes.io queried %d times, want 1", lk.calls)
	}
}

func TestUnknownTailFallsBackToRegAndRetriesHourly(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("N12345", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byReg: map[string][]adsb.Aircraft{}}
	lk := &fakeLookup{}
	p := &Poller{Store: st, ADSB: f, Airframes: lk, Now: func() time.Time { return now }, TrackingEvery: time.Second}
	p.Once(context.Background())
	now = now.Add(5 * time.Minute)
	p.Once(context.Background())
	if lk.calls != 1 || !reflect.DeepEqual(f.calls, []string{"reg:N12345", "reg:N12345"}) {
		t.Errorf("lookups=%d calls=%v", lk.calls, f.calls)
	}
	now = now.Add(time.Hour)
	p.Once(context.Background())
	if lk.calls != 2 {
		t.Errorf("no hourly retry: %d", lk.calls)
	}
}
