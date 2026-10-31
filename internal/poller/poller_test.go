package poller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
	"github.com/avanha/fullflight/internal/aerodatabox"
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
	err        error // returned by Hex
}

func (f *fake) Hex(_ context.Context, h string) ([]adsb.Aircraft, error) {
	f.calls = append(f.calls, "hex:"+h)
	return f.byHex[h], f.err
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
	e, _ := watchlist.Parse("B-LRX", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byReg: map[string][]adsb.Aircraft{"B-LRX": {ac(41)}}}
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

func newFlightRig(t *testing.T, dest string) *flightRig {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &flightRig{t: t, st: st, f: &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}},
		now: time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)}
	e, _ := watchlist.Parse("UA869 2026-10-31", r.now, time.Time{})
	e.Dest = dest
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
	r := newFlightRig(t, "")
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
	r := newFlightRig(t, "")
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
	r := newFlightRig(t, "")
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

// A US, Canadian or Australian registration needs no lookup: its hex follows from it, so the watch carries the hex from the start and is
// polled by it, and airframes.io is never asked.
func TestTailInARuleBasedCountryNeedsNoLookup(t *testing.T) {
	for tail, hex := range map[string]string{"N575UP": "A762CC", "C-FNOH": "C023C8", "C-GAZI": "C0473B", "VH-AMQ": "7C01C0"} {
		st, _ := store.Open(":memory:")
		now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
		e, _ := watchlist.Parse(tail, now, time.Time{})
		if e.Hex != hex {
			t.Errorf("%s: parsed with hex %q, want %q", tail, e.Hex, hex)
		}
		st.AddWatch(e)
		f := &fake{byHex: map[string][]adsb.Aircraft{hex: {ac(41)}}}
		lk := &fakeLookup{}
		p := &Poller{Store: st, ADSB: f, Airframes: lk, Now: func() time.Time { return now }}
		p.Once(context.Background())
		if lk.calls != 0 || !reflect.DeepEqual(f.calls, []string{"hex:" + hex}) {
			t.Errorf("%s: lookups=%d calls=%v", tail, lk.calls, f.calls)
		}
		st.Close()
	}
	// the other way: a hex watch learns its registration
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("hex:A762CC", now, time.Time{})
	if e.Tail != "N575UP" {
		t.Errorf("hex watch tail = %q", e.Tail)
	}
}

func TestUnknownTailFallsBackToRegAndRetriesHourly(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRX", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byReg: map[string][]adsb.Aircraft{}}
	lk := &fakeLookup{}
	p := &Poller{Store: st, ADSB: f, Airframes: lk, Now: func() time.Time { return now }, TrackingEvery: time.Second}
	p.Once(context.Background())
	now = now.Add(5 * time.Minute)
	p.Once(context.Background())
	if lk.calls != 1 || !reflect.DeepEqual(f.calls, []string{"reg:B-LRX", "reg:B-LRX"}) {
		t.Errorf("lookups=%d calls=%v", lk.calls, f.calls)
	}
	now = now.Add(time.Hour)
	p.Once(context.Background())
	if lk.calls != 2 {
		t.Errorf("no hourly retry: %d", lk.calls)
	}
}

// at moves a sighting to the given coordinates.
func at(a adsb.Aircraft, lat, lon float64) adsb.Aircraft {
	a.Lat, a.Lon = fl(lat), fl(lon)
	return a
}

var (
	denver = [2]float64{39.86, -104.67} // an intermediate stop
	sfo    = [2]float64{37.62, -122.37} // KSFO
)

func TestDestinationOverridesGrace(t *testing.T) {
	r := newFlightRig(t, "KSFO")
	up := at(flying(40, "UAL869"), 41, -90)
	r.step(0, &up)

	// A 6 hour stop at Denver (twice the plain 3 h grace) does not end it.
	den := at(parked("UAL869"), denver[0], denver[1])
	if e := r.step(time.Hour, &den); e.LandedAt.IsZero() {
		t.Fatalf("landing at Denver not recorded: %+v", e)
	}
	if e := r.step(6*time.Hour, &den); e.Status != watchlist.StatusTracking {
		t.Fatalf("ended at an intermediate stop despite a destination: %+v", e)
	}
	// Even under a new callsign (a different flight number out of Denver).
	other := at(parked("UAL1234"), denver[0], denver[1])
	if e := r.step(10*time.Minute, &other); e.Status != watchlist.StatusTracking {
		t.Fatalf("callsign change ended a watch that has a destination: %+v", e)
	}
	up2 := at(flying(40, "UAL869"), 39, -110)
	if e := r.step(10*time.Minute, &up2); !e.LandedAt.IsZero() || e.Status != watchlist.StatusTracking {
		t.Fatalf("second leg: %+v", e)
	}
	// Touches down at San Francisco: done at once, final position recorded.
	sf := at(parked("UAL869"), sfo[0]+0.01, sfo[1])
	e := r.step(2*time.Hour, &sf)
	if e.Status != watchlist.StatusDone {
		t.Fatalf("not done on arrival: %+v", e)
	}
	if last, ok := r.st.LastPosition("A1B2C3"); !ok || last.Lat != sfo[0]+0.01 {
		t.Errorf("arrival position not stored: %+v", last)
	}
}

func TestDestinationLandingNearbyButElsewhere(t *testing.T) {
	r := newFlightRig(t, "KSFO")
	up := at(flying(40, "UAL869"), 40, -115)
	r.step(0, &up)
	// Lands at Sacramento (~75 nm from SFO), well outside the arrival radius.
	smf := at(parked("UAL869"), 38.70, -121.59)
	if e := r.step(time.Hour, &smf); e.Status != watchlist.StatusTracking {
		t.Fatalf("diversion treated as arrival: %+v", e)
	}
}

func TestDestinationDiversionEventuallyEnds(t *testing.T) {
	r := newFlightRig(t, "KSFO")
	up := at(flying(40, "UAL869"), 40, -115)
	r.step(0, &up)
	smf := at(parked("UAL869"), 38.70, -121.59)
	r.step(time.Hour, &smf)
	if e := r.step(11*time.Hour, &smf); e.Status != watchlist.StatusTracking {
		t.Fatalf("ended before 4x grace (12 h): %+v", e)
	}
	if e := r.step(2*time.Hour, &smf); e.Status != watchlist.StatusDone {
		t.Fatalf("diverted aircraft never released: %+v", e)
	}
}

func TestDestinationLostOnApproach(t *testing.T) {
	r := newFlightRig(t, "KSFO")
	up := at(flying(40, "UAL869"), 37.7, -122.2)
	lowAlt := 2400.0
	up.AltBaro = adsb.AltBaro{Feet: &lowAlt}
	r.step(0, &up) // low, ~10 nm out, descending

	// Gone from the feed. Not yet: could be a coverage blip.
	if e := r.step(5*time.Minute, nil); e.Status != watchlist.StatusTracking {
		t.Fatalf("ended after 5 minutes: %+v", e)
	}
	if e := r.step(6*time.Minute, nil); e.Status != watchlist.StatusDone {
		t.Fatalf("not done after vanishing on approach: %+v", e)
	}
}

func TestVanishedHighIsNotArrival(t *testing.T) {
	r := newFlightRig(t, "KSFO")
	up := at(flying(40, "UAL869"), 37.7, -122.3) // overhead SFO at 35,000 ft
	r.step(0, &up)
	if e := r.step(30*time.Minute, nil); e.Status != watchlist.StatusTracking {
		t.Fatalf("a high-altitude dropout ended the watch: %+v", e)
	}
}

func TestDatelessCallsignWatchLatchesAndFollows(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)
	e, err := watchlist.Parse("CPA113", now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	st.AddWatch(e)
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	lk := &fakeLookup{}
	p := &Poller{Store: st, ADSB: f, Airframes: lk, Now: func() time.Time { return now }, WaitingEvery: time.Second, TrackingEvery: time.Second}

	p.Once(context.Background())
	if !reflect.DeepEqual(f.calls, []string{"cs:CPA113", "cs:CPA0113"}) {
		t.Fatalf("calls = %v", f.calls)
	}
	if lk.calls != 0 {
		t.Errorf("a callsign was looked up as a registration %d times", lk.calls)
	}
	a := ac(22)
	a.Flight, a.Hex = "CPA113  ", "780AA4"
	f.byCallsign["CPA113"] = []adsb.Aircraft{a}
	now = now.Add(time.Minute)
	p.Once(context.Background())
	if w, _ := st.ListWatches(true); w[0].Hex != "780AA4" || w[0].Status != watchlist.StatusTracking || w[0].Callsign != "CPA113" {
		t.Errorf("entry = %+v", w[0])
	}
}

func TestUnknownAirlineCallsignIsNotLookedUpAsATail(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)
	for _, in := range []string{"TZP1", "CKK624"} {
		e, err := watchlist.Parse(in, now, time.Time{})
		if err != nil || e.Kind != watchlist.KindFlight {
			t.Fatalf("%s: %+v %v", in, e, err)
		}
		st.AddWatch(e)
	}
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}, byReg: map[string][]adsb.Aircraft{}}
	lk := &fakeLookup{}
	p := &Poller{Store: st, ADSB: f, Airframes: lk, Now: func() time.Time { return now }, WaitingEvery: time.Second}
	p.Once(context.Background())
	if lk.calls != 0 {
		t.Errorf("airframes.io was asked %d times about a registration", lk.calls)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "reg:") {
			t.Errorf("looked up by registration: %v", f.calls)
		}
	}
	want := map[string]bool{"cs:TZP1": true, "cs:TZP0001": true, "cs:CKK624": true, "cs:CKK0624": true}
	for _, c := range f.calls {
		delete(want, c)
	}
	if len(want) != 0 {
		t.Errorf("callsign lookups missing %v; made %v", want, f.calls)
	}
}

type fakeProvider struct {
	byDate map[string][]aerodatabox.Flight
	calls  []string
}

func (f *fakeProvider) ByNumber(_ context.Context, number, date string) ([]aerodatabox.Flight, error) {
	f.calls = append(f.calls, number+"/"+date)
	return f.byDate[date], nil
}

// A flight ADS-B cannot see (mid-ocean) is found by number: the watch latches onto the aircraft, takes the
// destination, stores the provider's position under its own source, and asks again only every ProviderEvery.
func TestProviderFindsAFlightADSBCannot(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 21, 30, 0, 0, time.UTC)
	e, _ := watchlist.Parse("CKK624", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	gs, trk := 466.0, 311.0
	pr := &fakeProvider{byDate: map[string][]aerodatabox.Flight{"2026-10-30": {{
		Number: "CK 624", Callsign: "CKK624", Status: "EnRoute", Hex: "781D8D", Reg: "B-221W", From: "KLAX", To: "ZSPD",
		HasPos: true, Lat: 58.3, Lon: -143.8, PosAt: now.Add(-20 * time.Minute), GS: &gs, Track: &trk}}}}
	p := &Poller{Store: st, ADSB: f, Flights: pr, Now: func() time.Time { return now }, WaitingEvery: time.Second, TrackingEvery: time.Second}

	p.Once(context.Background())
	w, _ := st.ListWatches(true)
	if w[0].Hex != "781D8D" || w[0].Tail != "B-221W" || w[0].Callsign != "CKK624" || w[0].Dest != "ZSPD" ||
		w[0].Status != watchlist.StatusTracking || !w[0].Airborne {
		t.Fatalf("entry = %+v", w[0])
	}
	if !reflect.DeepEqual(pr.calls, []string{"CK624/2026-10-30"}) {
		t.Errorf("calls = %v", pr.calls)
	}
	if pos, ok := st.LastPosition("781D8D"); !ok || pos.Source != aerodatabox.Source || pos.Lat != 58.3 || *pos.GS != 466 {
		t.Errorf("position = %+v %v", pos, ok)
	}
	if hex, ok := st.HexForTail("B-221W"); !ok || hex != "781D8D" {
		t.Errorf("airframe table: %q %v", hex, ok)
	}

	// still no ADS-B a minute later: no new request; after ProviderEvery, one
	now = now.Add(time.Minute)
	p.Once(context.Background())
	if len(pr.calls) != 1 {
		t.Errorf("asked again after a minute: %v", pr.calls)
	}
	now = now.Add(30 * time.Minute)
	p.Once(context.Background())
	if len(pr.calls) != 2 {
		t.Errorf("calls after 31 min = %v", pr.calls)
	}
}

// A flight that left yesterday (UTC) is found on the second try, and a flight not yet in the air is not latched.
func TestProviderTriesYesterdayAndIgnoresGroundedFlights(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("CKK624", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	pr := &fakeProvider{byDate: map[string][]aerodatabox.Flight{
		"2026-10-30": {{Status: "Expected", Hex: "AAAAAA"}},
		"2026-10-29": {{Status: "EnRoute", Hex: "781D8D", Reg: "B-221W", To: "ZSPD"}}}}
	p := &Poller{Store: st, ADSB: f, Flights: pr, Now: func() time.Time { return now }, WaitingEvery: time.Second}
	p.Once(context.Background())
	if w, _ := st.ListWatches(true); w[0].Hex != "781D8D" {
		t.Errorf("entry = %+v", w[0])
	}
	if !reflect.DeepEqual(pr.calls, []string{"CK624/2026-10-30", "CK624/2026-10-29"}) {
		t.Errorf("calls = %v", pr.calls)
	}
	// no position from the provider: nothing stored
	if _, ok := st.LastPosition("781D8D"); ok {
		t.Error("a position appeared from nowhere")
	}
}

// With ADS-B working the provider is never asked, and it never overrides an aircraft already latched.
func TestProviderIsAFallbackOnly(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("CKK624", now, time.Time{})
	st.AddWatch(e)
	a := ac(34)
	a.Flight, a.Hex = "CKK624", "781D8D"
	f := &fake{byCallsign: map[string][]adsb.Aircraft{"CKK624": {a}}, byHex: map[string][]adsb.Aircraft{"781D8D": {a}}}
	pr := &fakeProvider{byDate: map[string][]aerodatabox.Flight{"2026-10-30": {{Status: "EnRoute", Hex: "BBBBBB"}}}}
	p := &Poller{Store: st, ADSB: f, Flights: pr, Now: func() time.Time { return now }, WaitingEvery: time.Second, TrackingEvery: time.Second}
	p.Once(context.Background())
	if len(pr.calls) != 0 {
		t.Errorf("provider asked while ADS-B was working: %v", pr.calls)
	}
	// ADS-B goes quiet; the provider names a different aircraft: ignore it
	delete(f.byHex, "781D8D")
	now = now.Add(time.Hour)
	p.Once(context.Background())
	if w, _ := st.ListWatches(true); w[0].Hex != "781D8D" {
		t.Errorf("latched aircraft replaced: %+v", w[0])
	}
}

type fakeSearch struct {
	ms    []airframes.Message
	calls []string
}

func (f *fakeSearch) SearchText(_ context.Context, q string) ([]airframes.Message, error) {
	f.calls = append(f.calls, q)
	return f.ms, nil
}

func textMsg(at time.Time, hex, tail, text string) airframes.Message {
	m := airframes.Message{Timestamp: at, Text: airframes.FlexString(text)}
	m.Airframe.ICAO, m.Airframe.Tail = airframes.FlexString(hex), airframes.FlexString(tail)
	return m
}

// With nothing else able to find a flight, its number in the text of recent messages names the aircraft:
// the newest recent match wins, an older flight of the same number does not, and the search is throttled.
func TestFlightFoundInMessageText(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("UPS58", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	se := &fakeSearch{ms: []airframes.Message{
		textMsg(now.Add(-2*time.Minute), "A762CC", "N575UP", "OAKODYA.AFN/FMHUPS58,.N575UP,,200552/FAK0,KZAK"),
		textMsg(now.Add(-time.Minute), "A11111", "N111XX", "FREIGHT UPS580 PHNL/VHHH"),                 // another flight number
		textMsg(now.Add(-26*time.Hour), "A76DF1", "N578UP", "B N578UP 29OCT26 1942 UPS58   PHNL/VHHH"), // yesterday
		textMsg(now.Add(-time.Minute), "", "", "UPS58 with no airframe named"),
	}}
	p := &Poller{Store: st, ADSB: f, Search: se, Now: func() time.Time { return now }, WaitingEvery: time.Second, TrackingEvery: time.Second}

	p.Once(context.Background())
	if !reflect.DeepEqual(se.calls, []string{"UPS58"}) {
		t.Fatalf("searched %v", se.calls)
	}
	w, _ := st.ListWatches(true)
	if w[0].Hex != "A762CC" || w[0].Tail != "N575UP" || w[0].Status != watchlist.StatusTracking {
		t.Fatalf("entry = %+v", w[0])
	}
	if hex, ok := st.HexForTail("N575UP"); !ok || hex != "A762CC" {
		t.Errorf("airframe table: %q %v", hex, ok)
	}
	now = now.Add(time.Minute)
	p.Once(context.Background())
	if len(se.calls) != 1 {
		t.Errorf("searched again after latching: %v", se.calls)
	}
}

func TestMessageSearchThrottledAndNeverOverrides(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("UPS58", now, time.Time{})
	st.AddWatch(e)
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	se := &fakeSearch{} // nothing yet
	p := &Poller{Store: st, ADSB: f, Search: se, Now: func() time.Time { return now }, WaitingEvery: time.Second}
	p.Once(context.Background())
	now = now.Add(5 * time.Minute)
	p.Once(context.Background())
	if len(se.calls) != 1 {
		t.Errorf("searched %d times in 5 minutes", len(se.calls))
	}
	now = now.Add(6 * time.Minute)
	se.ms = []airframes.Message{textMsg(now.Add(-time.Minute), "A762CC", "N575UP", "x UPS58 x")}
	p.Once(context.Background())
	if w, _ := st.ListWatches(true); w[0].Hex != "A762CC" || len(se.calls) != 2 {
		t.Errorf("entry %+v, searches %d", w[0], len(se.calls))
	}
	// ADS-B works for a watch: no search at all
	st2, _ := store.Open(":memory:")
	defer st2.Close()
	e2, _ := watchlist.Parse("CKK624", now, time.Time{})
	st2.AddWatch(e2)
	a := ac(34)
	a.Flight, a.Hex = "CKK624", "781D8D"
	f2 := &fake{byCallsign: map[string][]adsb.Aircraft{"CKK624": {a}}, byHex: map[string][]adsb.Aircraft{}}
	se2 := &fakeSearch{}
	p2 := &Poller{Store: st2, ADSB: f2, Search: se2, Now: func() time.Time { return now }, WaitingEvery: time.Second}
	p2.Once(context.Background())
	if len(se2.calls) != 0 {
		t.Errorf("searched although ADS-B had it: %v", se2.calls)
	}
}

func TestNamesCallsign(t *testing.T) {
	for text, want := range map[string]bool{"FMHUPS58,.N575UP": true, "UPS58": true, "UPS580": false, "UPS5": false, "xUPS581 UPS58": true, "ups58 pg1": true, "": false} {
		if got := namesCallsign(text, "UPS58"); got != want {
			t.Errorf("%q: %v", text, got)
		}
	}
}

// An airline missing from the table (Fiji Airways) is looked up by its ICAO callsign, which the provider accepts;
// a two-letter flight number for such an airline cannot be and is left alone.
func TestProviderTakesCallsignOfUnknownAirline(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 21, 30, 0, 0, time.UTC)
	for _, in := range []string{"FJI911", "flight:XQ911"} {
		e, _ := watchlist.Parse(in, now, time.Time{})
		st.AddWatch(e)
	}
	f := &fake{byCallsign: map[string][]adsb.Aircraft{}, byHex: map[string][]adsb.Aircraft{}}
	pr := &fakeProvider{byDate: map[string][]aerodatabox.Flight{"2026-10-30": {{Status: "EnRoute", Hex: "C8809D", Reg: "DQ-FAJ", To: "YSSY"}}}}
	p := &Poller{Store: st, ADSB: f, Flights: pr, Now: func() time.Time { return now }, WaitingEvery: time.Second}
	p.Once(context.Background())
	if len(pr.calls) == 0 || pr.calls[0] != "FJI911/2026-10-30" {
		t.Fatalf("calls = %v", pr.calls)
	}
	for _, c := range pr.calls {
		if strings.HasPrefix(c, "XQ") {
			t.Errorf("asked for a flight number we cannot convert: %v", pr.calls)
		}
	}
}

// An aircraft that is not transmitting (parked, landed, out of coverage) is asked about a quarter as often as one that is:
// asking every source every 45 s about silence is most of the traffic, and the feeds rate-limit.
func TestQuietAircraftArePolledLess(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	for _, id := range []string{"hex:AAAAAA", "hex:BBBBBB"} {
		e, _ := watchlist.Parse(id, now, time.Time{})
		st.AddWatch(e)
	}
	live := ac(41)
	live.Hex = "AAAAAA"
	f := &fake{byHex: map[string][]adsb.Aircraft{"AAAAAA": {live}}} // BBBBBB is heard from never
	p := &Poller{Store: st, ADSB: f, Now: func() time.Time { return now }, TrackingEvery: time.Second}
	count := func(h string) (n int) {
		for _, c := range f.calls {
			if c == "hex:"+h {
				n++
			}
		}
		return
	}
	for i := 0; i < 8; i++ { // eight seconds, one poll opportunity each
		p.Once(context.Background())
		now = now.Add(time.Second)
	}
	if a, b := count("AAAAAA"), count("BBBBBB"); a != 8 || b != 2 {
		t.Errorf("polls in 8 s: transmitting %d (want 8), silent %d (want 2: at once, then every four)", a, b)
	}
	// Once a silent aircraft is heard it is polled at the normal rate again.
	f.byHex["BBBBBB"] = []adsb.Aircraft{func() adsb.Aircraft { a := ac(40); a.Hex = "BBBBBB"; return a }()}
	f.calls = nil
	for i := 0; i < 8; i++ {
		p.Once(context.Background())
		now = now.Add(time.Second)
	}
	if b := count("BBBBBB"); b < 6 {
		t.Errorf("after being heard it was polled only %d times in 8 s", b)
	}
}

// A paused watch costs no requests at all.
func TestPausedWatchIsNotPolled(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("hex:AAAAAA", now, time.Time{})
	e, _ = st.AddWatch(e)
	live := ac(41)
	live.Hex = "AAAAAA"
	f := &fake{byHex: map[string][]adsb.Aircraft{"AAAAAA": {live}}}
	p := &Poller{Store: st, ADSB: f, Now: func() time.Time { return now }, TrackingEvery: time.Second}
	p.Once(context.Background())
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	e, _ = st.GetWatch(e.ID)
	e.Pause()
	st.UpdateWatch(e)
	f.calls = nil
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		p.Once(context.Background())
	}
	if len(f.calls) != 0 {
		t.Errorf("a paused watch was polled: %v", f.calls)
	}
	e, _ = st.GetWatch(e.ID)
	e.Resume(now)
	st.UpdateWatch(e)
	now = now.Add(time.Minute)
	p.Once(context.Background())
	if len(f.calls) != 1 {
		t.Errorf("after resuming: %v", f.calls)
	}
}

// What asking about a watch found is recorded, so the page can say why there is no track.
func TestPollDiagnostics(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("hex:AAAAAA", now, time.Time{})
	e, _ = st.AddWatch(e)
	f := &fake{byHex: map[string][]adsb.Aircraft{}}
	p := &Poller{Store: st, ADSB: f, Now: func() time.Time { return now }, TrackingEvery: time.Second}
	ask := func() store.PollState {
		now = now.Add(10 * time.Second)
		p.Once(context.Background())
		all, err := st.PollStates()
		if err != nil {
			t.Fatal(err)
		}
		return all[e.ID]
	}
	check := func(step string, s store.PollState, outcome string, misses int, heard bool) {
		t.Helper()
		if s.Outcome != outcome || s.Misses != misses || s.Heard.IsZero() == heard || !s.Asked.Equal(now) {
			t.Errorf("%s: %+v (now %v), want %s, %d misses, heard=%v", step, s, now, outcome, misses, heard)
		}
	}

	// The sources answer and it is not there.
	check("empty answer", ask(), "not_seen", 1, false)
	check("again", ask(), "not_seen", 2, false)

	// It is in the answer, with no position, then with an old one.
	a := ac(41)
	a.Hex, a.Lat, a.Lon = "AAAAAA", nil, nil
	f.byHex["AAAAAA"] = []adsb.Aircraft{a}
	s := ask()
	check("no position", s, "no_position", 3, false)
	if !strings.Contains(s.Detail, "no position") || !strings.Contains(s.Detail, "adsb.lol") {
		t.Errorf("detail = %q", s.Detail)
	}
	a = ac(41)
	a.Hex, a.SeenPos = "AAAAAA", fl(300)
	f.byHex["AAAAAA"] = []adsb.Aircraft{a}
	s = ask()
	check("old position", s, "no_position", 4, false)
	if !strings.Contains(s.Detail, "300 s old") {
		t.Errorf("detail = %q", s.Detail)
	}

	// A failed ask says nothing about the aircraft: it is not a miss, and the error is kept.
	f.err = errors.New("adsb.lol: HTTP 429\nadsb.fi: HTTP 503")
	s = ask()
	check("error", s, "error", 4, false)
	if s.Detail != "adsb.lol: HTTP 429" {
		t.Errorf("error detail = %q", s.Detail)
	}
	f.err = nil

	// Heard: when its position was taken, and by whom; the misses are cleared.
	a = ac(41)
	a.Hex = "AAAAAA"
	f.byHex["AAAAAA"] = []adsb.Aircraft{a}
	s = ask()
	if s.Outcome != "heard" || s.Misses != 0 || s.Source != "adsb.lol" || !s.Heard.Equal(now.Add(-2*time.Second)) {
		t.Errorf("heard: %+v (now %v)", s, now)
	}
	heardAt := s.Heard

	// Gone again: the last time it was heard is kept, and the misses start over.
	f.byHex["AAAAAA"] = nil
	s = ask()
	if s.Outcome != "not_seen" || s.Misses != 1 || !s.Heard.Equal(heardAt) || s.Source != "adsb.lol" {
		t.Errorf("gone again: %+v", s)
	}

	// Pausing stops the asking: the state stays as it was.
	w, _ := st.GetWatch(e.ID)
	w.Pause()
	st.UpdateWatch(w)
	before := s
	now = now.Add(time.Hour)
	p.Once(context.Background())
	if all, _ := st.PollStates(); all[e.ID] != before {
		t.Errorf("a paused watch was asked about: %+v", all[e.ID])
	}
	// And it goes with the watch.
	st.DeleteWatch(e.ID)
	if all, _ := st.PollStates(); len(all) != 0 {
		t.Errorf("state outlived its watch: %+v", all)
	}
}
