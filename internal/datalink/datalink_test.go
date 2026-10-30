package datalink

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

type fx struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Text     string    `json:"text"`
	Received time.Time `json:"received"`
}

func fixture(t *testing.T, name string) fx {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var all []fx
	json.Unmarshal(b, &all)
	for _, f := range all {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no fixture %q", name)
	return fx{}
}

func msg(id int64, f fx) airframes.Message {
	return airframes.Message{ID: id, Timestamp: f.Received, Label: airframes.FlexString(f.Label), Text: airframes.FlexString(f.Text)}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestIngestADSC(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "adsc_takeoff_sea") // embedded :09:30, received 19:22
	raw := msg(1, f)
	raw.Station.Ident = "KK7JPR-BAINBRIDGE"
	if ok, err := Ingest(st, "780AB8", raw); !ok || err != nil {
		t.Fatalf("ingest: %v %v", ok, err)
	}
	ms, _ := st.Messages("780AB8", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 10)
	if len(ms) != 1 {
		t.Fatalf("stored %d", len(ms))
	}
	m := ms[0]
	if m.Kind != "adsc" || m.Tail != "B-LRM" || m.Direction != "down" || m.Text != f.Text || m.Station != "KK7JPR-BAINBRIDGE" {
		t.Errorf("message = %+v", m)
	}
	if m.Lat == nil || *m.Lat < 47.41 || *m.AltFt != 1868 {
		t.Errorf("position = %v %v", m.Lat, m.AltFt)
	}
	if want := time.Date(2026, 10, 27, 19, 9, 30, 0, time.UTC); m.PosT == nil || !m.PosT.Equal(want) {
		t.Errorf("pos time = %v, want %v (not the 19:22 receipt time)", m.PosT, want)
	}
	if !strings.Contains(m.Summary, "ADS-C report 47.419N") || !strings.Contains(m.Detail, "Basic report") || m.Route == "" || m.Mismatch != "" {
		t.Errorf("summary=%q mismatch=%q route=%q", m.Summary, m.Mismatch, m.Route)
	}
}

func TestIngestSkipsEmptyAndDedupesStations(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "cpdlc_wilco")
	if ok, _ := Ingest(st, "780AA4", airframes.Message{ID: 9, Timestamp: f.Received, Label: "BA"}); ok {
		t.Error("message with no text was stored")
	}
	if ok, _ := Ingest(st, "780AA4", msg(10, f)); !ok {
		t.Fatal("first reception not stored")
	}
	second := msg(11, f) // same text heard by another station 4 s later
	second.Timestamp = f.Received.Add(4 * time.Second)
	if ok, _ := Ingest(st, "780AA4", second); ok {
		t.Error("duplicate reception stored as a new message")
	}
	if ok, _ := Ingest(st, "780AA4", msg(10, f)); ok { // refetched after the overlap
		t.Error("refetched message stored twice")
	}
	ms, _ := st.Messages("780AA4", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 10)
	if len(ms) != 1 || ms[0].RxCount != 2 {
		t.Fatalf("messages = %+v", ms)
	}
	// The same text a day later is a different message (a repeated WILCO).
	later := msg(12, f)
	later.Timestamp = f.Received.Add(24 * time.Hour)
	if ok, _ := Ingest(st, "780AA4", later); !ok {
		t.Error("same text a day later was treated as a duplicate")
	}
}

func TestIngestMismatchAndOwnership(t *testing.T) {
	st := newStore(t)
	st.RecordAirframe(store.Airframe{Hex: "780AA4", Tail: "B-LRD", Source: "test"})
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Source: "test"})

	// The handoff's case: text says CX111 / B-LRD, airframes.io says CX852 / B-LRM.
	f := fixture(t, "rep081_handoff")
	raw := msg(1, f)
	raw.Tail, raw.FlightNumber = "B-LRM", "CX852"
	if _, err := Ingest(st, "780AA4", raw); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages("780AA4", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 10)
	m := ms[0]
	if m.Tail != "B-LRD" || m.Flight != "CX111" {
		t.Errorf("our identity must come from the text: %+v", m)
	}
	if m.AFTail != "B-LRM" || m.AFFlight != "CX852" {
		t.Errorf("airframes.io hints not kept apart: %+v", m)
	}
	for _, want := range []string{"tail: airframes.io says B-LRM, text says B-LRD", "flight: airframes.io says CX852, text says CX111"} {
		if !strings.Contains(m.Mismatch, want) {
			t.Errorf("mismatch %q missing %q", m.Mismatch, want)
		}
	}

	// Fetched under the wrong hex: the text names B-LRD but we asked for B-LRM's.
	g := fixture(t, "cpdlc_wilco") // B-LRD
	if _, err := Ingest(st, "780AB8", msg(2, g)); err != nil {
		t.Fatal(err)
	}
	ms, _ = st.Messages("780AB8", g.Received.Add(-time.Hour), g.Received.Add(time.Hour), 10)
	if !strings.Contains(ms[0].Mismatch, "text names B-LRD, which is hex 780AA4, not 780AB8") {
		t.Errorf("ownership mismatch = %q", ms[0].Mismatch)
	}

	// Agreement produces no warning, and zero-padded flight numbers compare equal.
	ok := msg(3, fixture(t, "real_cpdlc_prefixed"))
	ok.Timestamp = fixture(t, "real_cpdlc_prefixed").Received
	ok.Tail, ok.Flight.Flight = "B-LRD", "CX113"
	Ingest(st, "780AA4", ok)
	ms, _ = st.Messages("780AA4", ok.Timestamp.Add(-time.Minute), ok.Timestamp.Add(time.Minute), 10)
	if len(ms) != 1 || ms[0].Mismatch != "" {
		t.Errorf("agreeing hints produced %+v", ms)
	}
}

func TestSuspectPosition(t *testing.T) {
	st := newStore(t)
	// ADS-B puts the aircraft over Seattle two days before the message's resolved time.
	f := fixture(t, "adsc_takeoff_sea")
	st.AddPosition(store.Position{Hex: "780AB8", T: time.Date(2026, 10, 27, 19, 8, 0, 0, time.UTC), Lat: 47.4, Lon: -122.3, Source: "adsb.lol"})
	if _, err := Ingest(st, "780AB8", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages("780AB8", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 10)
	if ms[0].Suspect != "" {
		t.Errorf("consistent fix flagged: %q", ms[0].Suspect)
	}

	// The same ADS-C text from a backlog station arrives stamped hours later: its resolved
	// time lands near a fix 5,000+ nm away, so it implies an impossible speed.
	st2 := newStore(t)
	st2.AddPosition(store.Position{Hex: "780AB8", T: time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC), Lat: 22.3, Lon: 113.9, Source: "adsb.lol"})
	if _, err := Ingest(st2, "780AB8", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	ms, _ = st2.Messages("780AB8", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 10)
	if !strings.Contains(ms[0].Suspect, "kt against the adsb fix") {
		t.Errorf("suspect = %q", ms[0].Suspect)
	}
}

type fakeSource struct {
	msgs  []airframes.Message // all history, any order
	calls []struct{ since, until time.Time }
	err   error
}

func (s *fakeSource) Messages(_ context.Context, _ string, since, until time.Time) ([]airframes.Message, error) {
	s.calls = append(s.calls, struct{ since, until time.Time }{since, until})
	if s.err != nil {
		return nil, s.err
	}
	var out []airframes.Message
	for i := len(s.msgs) - 1; i >= 0; i-- { // newest first, as the API returns them
		m := s.msgs[i]
		if !m.Timestamp.Before(since) && !m.Timestamp.After(until) {
			out = append(out, m)
		}
	}
	if len(out) > airframes.PageSize {
		out = out[:airframes.PageSize]
	}
	return out, nil
}

func TestFetcherPagesBackfillsAndResumes(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	e.Hex = "780AB8"
	st.AddWatch(e)

	// 250 distinct media advisories over the last 5 days, so three pages.
	src := &fakeSource{}
	for i := 0; i < 250; i++ {
		ts := now.Add(-time.Duration(250-i) * 25 * time.Minute)
		text := "0ES" + ts.Format("150405") + "VS"
		src.msgs = append(src.msgs, airframes.Message{ID: int64(1000 + i), Timestamp: ts, Label: "SA", Text: airframes.FlexString(text)})
	}
	cur := now
	f := &Fetcher{Store: st, Source: src, Now: func() time.Time { return cur }, Interval: time.Minute}
	f.Once(context.Background())

	ms, _ := st.Messages("780AB8", now.Add(-30*24*time.Hour), now.Add(time.Hour), 1000)
	if len(ms) != 250 {
		t.Fatalf("stored %d of 250", len(ms))
	}
	if len(src.calls) != 3 {
		t.Errorf("made %d requests, want 3 pages", len(src.calls))
	}
	if src.calls[0].since != now.Add(-7*24*time.Hour) {
		t.Errorf("backfill since = %v", src.calls[0].since)
	}
	if got, _ := st.FetchedTo("780AB8"); !got.Equal(now) {
		t.Errorf("fetched_to = %v", got)
	}

	// Next cycle starts 30 minutes before where the last one ended and takes one request.
	src.calls = nil
	cur = now.Add(5 * time.Minute)
	f.Once(context.Background())
	if len(src.calls) != 1 || !src.calls[0].since.Equal(now.Add(-30*time.Minute)) {
		t.Errorf("calls = %+v", src.calls)
	}

	// Not due again within Interval.
	src.calls = nil
	cur = cur.Add(10 * time.Second)
	f.Once(context.Background())
	if len(src.calls) != 0 {
		t.Errorf("refetched too soon: %+v", src.calls)
	}
}

func TestFetcherErrorsKeepState(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	e.Hex = "780AB8"
	st.AddWatch(e)
	src := &fakeSource{err: &airframes.RateLimitError{RetryAfter: 2 * time.Minute}}
	cur := now
	f := &Fetcher{Store: st, Source: src, Now: func() time.Time { return cur }, Interval: time.Second}
	f.Once(context.Background())
	if _, ok := st.FetchedTo("780AB8"); ok {
		t.Error("fetched_to advanced after a failed fetch")
	}
	// Paused for the Retry-After, then retries.
	src.calls = nil
	cur = now.Add(time.Minute)
	f.Once(context.Background())
	if len(src.calls) != 0 {
		t.Error("fetched during the rate-limit pause")
	}
	src.err = nil
	cur = now.Add(3 * time.Minute)
	f.Once(context.Background())
	if len(src.calls) == 0 {
		t.Error("did not resume after the pause")
	}
	if _, ok := st.FetchedTo("780AB8"); !ok {
		t.Error("fetched_to not set after a good fetch")
	}
}

func TestFetcherOnlyWatchedHexes(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	flight, _ := watchlist.Parse("UA869 2026-10-31", now, time.Time{}) // waiting, no hex yet
	st.AddWatch(flight)
	src := &fakeSource{}
	f := &Fetcher{Store: st, Source: src, Now: func() time.Time { return now }}
	f.Once(context.Background())
	if len(src.calls) != 0 {
		t.Errorf("fetched for an unmatched flight: %+v", src.calls)
	}
}
