package poller

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
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
