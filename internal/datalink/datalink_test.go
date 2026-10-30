package datalink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/decoder"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

type fx struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Text     string    `json:"text"`
	Received time.Time `json:"received"`
	FromHex  string    `json:"fromHex"`
	ToHex    string    `json:"toHex"`
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
	return airframes.Message{ID: id, Timestamp: f.Received, Label: airframes.FlexString(f.Label), Text: airframes.FlexString(f.Text),
		FromHex: airframes.FlexString(f.FromHex), ToHex: airframes.FlexString(f.ToHex)}
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

func TestIngestCPDLCPositionOnMap(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "cpdlc_position") // received 16:07, position time 16:06
	if _, err := Ingest(st, "780AB8", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages("780AB8", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 10)
	m := ms[0]
	if m.Lat == nil || *m.Lat < 19.17 || *m.Lat > 19.18 || m.Lon == nil || *m.Lon < 115.87 || *m.AltFt != 37000 {
		t.Fatalf("position = %v %v %v", m.Lat, m.Lon, m.AltFt)
	}
	if want := time.Date(2026, 10, 28, 16, 6, 0, 0, time.UTC); m.PosT == nil || !m.PosT.Equal(want) {
		t.Errorf("pos time = %v, want %v", m.PosT, want)
	}
	if !strings.HasPrefix(m.Summary, "CPDLC position report 19.172N") || m.Suspect != "" {
		t.Errorf("summary=%q suspect=%q", m.Summary, m.Suspect)
	}
}

func TestRedecodePicksUpDecoderChanges(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "cpdlc_position")
	// A row as an older decoder left it: raw text only, nothing derived.
	st.AddMessage(store.Message{ID: 1, Hex: "780AB8", T: f.Received, Label: f.Label, Text: f.Text, Kind: "cpdlc", Summary: "CPDLC downlink: POSITION REPORT"})
	st.AddMessage(store.Message{ID: 2, Hex: "780AB8", T: f.Received.Add(time.Hour), Label: "SA", Text: "0ES124056VS"})

	n, err := Redecode(st)
	if err != nil || n != 2 {
		t.Fatalf("changed %d, err %v", n, err)
	}
	ms, _ := st.Messages("780AB8", f.Received.Add(-time.Hour), f.Received.Add(3*time.Hour), 10)
	if ms[0].Lat == nil || !strings.HasPrefix(ms[0].Summary, "CPDLC position report 19.172N") || ms[0].Text != f.Text {
		t.Errorf("not redecoded or raw changed: %+v", ms[0])
	}
	if ms[1].Kind != "media_advisory" || ms[1].Direction != "down" {
		t.Errorf("second = %+v", ms[1])
	}
	if n, _ = Redecode(st); n != 0 {
		t.Errorf("second pass changed %d messages; should be idempotent", n)
	}
}

func TestDirectionOf(t *testing.T) {
	cases := []struct {
		afDir, from, to string
		want            decoder.Direction
	}{
		{"uplink", "", "", decoder.Uplink},
		{"downlink", "291085", "A48E3A", decoder.Downlink}, // airframes.io's word wins
		{"", "A48E3A", "260705", decoder.Downlink},
		{"", "291085", "a48e3a", decoder.Uplink},
		{"", "291085", "260705", decoder.Unknown},
		{"", "", "", decoder.Unknown},
	}
	for _, c := range cases {
		if got := directionOf(c.afDir, c.from, c.to, "A48E3A"); got != c.want {
			t.Errorf("%+v: got %v, want %v", c, got, c.want)
		}
	}
}

func TestIngestDeltaH1(t *testing.T) {
	st := newStore(t)
	up := fixture(t, "dal_cpdlc_noslash_uplink") // 291085 -> A48E3A
	if _, err := Ingest(st, "A48E3A", msg(1, up)); err != nil {
		t.Fatal(err)
	}
	pos := fixture(t, "dal_fms_posn")
	Ingest(st, "A48E3A", msg(2, pos))
	eng := fixture(t, "dal_engine_data")
	Ingest(st, "A48E3A", msg(3, eng))

	ms, _ := st.Messages("A48E3A", up.Received.Add(-24*time.Hour), up.Received.Add(24*time.Hour), 10)
	if len(ms) != 3 {
		t.Fatalf("stored %d", len(ms))
	}
	by := map[int64]store.Message{}
	for _, m := range ms {
		by[m.ID] = m
	}
	if m := by[1]; m.Kind != "cpdlc" || m.Direction != "up" || m.Tail != "N393DA" || m.FromHex != "291085" || m.ToHex != "A48E3A" {
		t.Errorf("uplink: %+v", m)
	}
	if m := by[2]; m.Kind != "fms_position" || m.Lat == nil || *m.Lat < 34.74 || !m.PosT.Equal(pos.Received.Truncate(time.Second)) {
		t.Errorf("posn: %+v", m)
	}
	if m := by[3]; m.Kind != "unknown" || !strings.HasPrefix(m.Summary, "Undecoded H1") {
		t.Errorf("engine: %+v", m)
	}

	// A stale decode (as an older build left it, e.g. unknown) is repaired by -redecode.
	st2 := newStore(t)
	st2.AddMessage(store.Message{ID: 1, Hex: "A48E3A", T: up.Received, Label: "H1", Text: up.Text, FromHex: "291085", ToHex: "A48E3A",
		Kind: "unknown", Summary: "Unrecognised H1 message", Direction: "down"})
	if n, err := Redecode(st2); err != nil || n != 1 {
		t.Fatalf("redecode: %d %v", n, err)
	}
	r, _ := st2.Messages("A48E3A", up.Received.Add(-time.Hour), up.Received.Add(time.Hour), 5)
	if r[0].Kind != "cpdlc" || r[0].Direction != "up" {
		t.Errorf("after redecode: %+v", r[0])
	}
}

// posn builds an FMS position report for the given degrees and minutes.
func posn(latDeg int, latMin float64, lonDeg int, lonMin float64) string {
	return fmt.Sprintf("POSN%02d%03dW%03d%03d,WPT,000000,350,NXT,000000,NX2,M50,270050,0", latDeg, int(latMin*10), lonDeg, int(lonMin*10))
}

func fmsMsg(id int64, at time.Time, text string) airframes.Message {
	return airframes.Message{ID: id, Timestamp: at, Label: "H1", Text: airframes.FlexString(text), FromHex: "A48E3A", ToHex: "260705"}
}

// The aircraft appears in southern California and, 25 minutes later, near Boston. Each run of
// fixes is coherent on its own, so neither should be flagged because the other exists, and the
// answer must not depend on which one was stored first.
func TestSuspectIsOrderIndependent(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 21, 17, 0, 0, time.UTC)
	ca := []airframes.Message{ // descending toward LAX, ~100 kt apart
		fmsMsg(1, t0, posn(34, 9.8, 115, 41.8)),
		fmsMsg(2, t0.Add(3*time.Minute), posn(34, 7.8, 115, 59.4)),
		fmsMsg(3, t0.Add(7*time.Minute), posn(34, 6.0, 116, 18.0)),
	}
	bos := []airframes.Message{
		fmsMsg(11, t0.Add(29*time.Minute), posn(41, 28.9, 72, 12.2)),
		fmsMsg(12, t0.Add(30*time.Minute), posn(41, 33.5, 71, 59.5)),
		fmsMsg(13, t0.Add(31*time.Minute), posn(41, 36.0, 71, 51.6)),
	}
	for name, order := range map[string][]airframes.Message{
		"newest first": {bos[2], bos[1], bos[0], ca[2], ca[1], ca[0]},
		"oldest first": {ca[0], ca[1], ca[2], bos[0], bos[1], bos[2]},
	} {
		st := newStore(t)
		for _, m := range order {
			if _, err := Ingest(st, "A48E3A", m); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Recheck(st, "A48E3A", t0.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		ms, _ := st.Messages("A48E3A", t0.Add(-time.Hour), t0.Add(2*time.Hour), 20)
		if len(ms) != 6 {
			t.Fatalf("%s: stored %d", name, len(ms))
		}
		for _, m := range ms {
			if m.Suspect != "" {
				t.Errorf("%s: message %d flagged: %s", name, m.ID, m.Suspect)
			}
		}
	}

	// A single fix in the wrong place between two good ones is an outlier.
	st := newStore(t)
	for _, m := range []airframes.Message{ca[0], ca[2], fmsMsg(99, t0.Add(5*time.Minute), posn(41, 28.9, 72, 12.2))} {
		Ingest(st, "A48E3A", m)
	}
	Recheck(st, "A48E3A", t0.Add(-time.Hour))
	ms, _ := st.Messages("A48E3A", t0.Add(-time.Hour), t0.Add(2*time.Hour), 20)
	for _, m := range ms {
		if (m.ID == 99) != (m.Suspect != "") {
			t.Errorf("message %d suspect=%q", m.ID, m.Suspect)
		}
	}
}

func TestRecheckClearsFlagsOnceNeighboursArrive(t *testing.T) {
	st := newStore(t)
	t0 := time.Date(2026, 10, 28, 21, 17, 0, 0, time.UTC)
	Ingest(st, "A48E3A", fmsMsg(1, t0, posn(34, 9.8, 115, 41.8)))
	// With one conflicting neighbour and nothing else to go on, the Boston fix is flagged.
	Ingest(st, "A48E3A", fmsMsg(2, t0.Add(20*time.Minute), posn(41, 28.9, 72, 12.2)))
	ms, _ := st.Messages("A48E3A", t0.Add(-time.Hour), t0.Add(2*time.Hour), 20)
	if ms[1].Suspect == "" {
		t.Fatalf("expected an interim flag, got none")
	}
	// The next report in the same run arrives; the earlier verdict is now stale.
	Ingest(st, "A48E3A", fmsMsg(3, t0.Add(21*time.Minute), posn(41, 33.5, 71, 59.5)))
	n, err := Recheck(st, "A48E3A", t0.Add(-time.Hour))
	if err != nil || n == 0 {
		t.Fatalf("recheck changed %d, err %v", n, err)
	}
	ms, _ = st.Messages("A48E3A", t0.Add(-time.Hour), t0.Add(2*time.Hour), 20)
	// The pair now vouch for each other; the lone California fix is the odd one out.
	for _, m := range ms {
		if want := m.ID == 1; (m.Suspect != "") != want {
			t.Errorf("message %d suspect=%q, want flagged=%v", m.ID, m.Suspect, want)
		}
	}
	// And nothing changes the second time.
	if n, _ := Recheck(st, "A48E3A", t0.Add(-time.Hour)); n != 0 {
		t.Errorf("second recheck changed %d", n)
	}
}

func TestCompareTimes(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 28, h, m, s, 0, time.UTC) }
	cases := []struct {
		name    string
		arrived time.Time
		embed   string
		flag    bool
		note    []string
	}{
		{"Boston run (healthy station)", at(21, 46, 32), "214212", false, []string{"arrived 21:46:32 Z", "21:42:12", "-4m20s"}},
		{"California run (hours apart)", at(21, 17, 17), "235530", true, []string{"+2h38m13s", "unverified"}},
		{"just inside tolerance", at(12, 0, 0), "125959", false, []string{"+59m59s"}[:0]},
		{"across midnight, close", at(23, 59, 0), "000100", false, []string{"+2m0s"}},
		{"across midnight, far", at(23, 59, 0), "060000", true, []string{"+6h1m0s"}},
		{"embedded hours behind", at(1, 0, 0), "230000", true, []string{"-2h0m0s"}},
	}
	cases[2].embed, cases[2].arrived = "120959", at(12, 0, 0) // 9m59s ahead: inside the 10 minute tolerance
	cases[2].note = []string{"+9m59s"}
	for _, c := range cases {
		note, flag := compareTimes(c.arrived, c.embed)
		if flag != c.flag {
			t.Errorf("%s: flag = %v, want %v (%s)", c.name, flag, c.flag, note)
		}
		for _, want := range c.note {
			if !strings.Contains(note, want) {
				t.Errorf("%s: note %q missing %q", c.name, note, want)
			}
		}
	}
	for _, bad := range []string{"", "12", "ab1234", "246100", "126100", "123499"} {
		if n, f := compareTimes(at(12, 0, 0), bad); n != "" || f {
			t.Errorf("%q: %q %v", bad, n, f)
		}
	}
}

func TestIngestFMSTimeNote(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "dal_fms_posn") // arrived 16:34:19Z, waypoint time 23:27:18: hours apart
	if _, err := Ingest(st, "A48E3A", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages("A48E3A", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 5)
	m := ms[0]
	if !m.TimeFlag || !strings.Contains(m.TimeNote, "23:27:18") || !strings.Contains(m.TimeNote, "unverified") {
		t.Errorf("flag=%v note=%q", m.TimeFlag, m.TimeNote)
	}
	// The position is still stored (and drawn), but it no longer counts as the aircraft's last position.
	if m.Lat == nil {
		t.Fatal("position dropped")
	}
	if _, ok := st.LastMessagePosition("A48E3A"); ok {
		t.Error("an unverified-time position was offered as the last position")
	}
	// Other message kinds carry no such note.
	st2 := newStore(t)
	w := fixture(t, "cpdlc_wilco")
	Ingest(st2, "780AA4", msg(1, w))
	ms, _ = st2.Messages("780AA4", w.Received.Add(-time.Hour), w.Received.Add(time.Hour), 5)
	if ms[0].TimeNote != "" || ms[0].TimeFlag {
		t.Errorf("cpdlc: %q %v", ms[0].TimeNote, ms[0].TimeFlag)
	}
}

func TestRedecodeFillsTimeNote(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "dal_fms_posn")
	st.AddMessage(store.Message{ID: 1, Hex: "A48E3A", T: f.Received, Label: "H1", Text: f.Text, FromHex: "A48E3A", ToHex: "260705"})
	if n, err := Redecode(st); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	ms, _ := st.Messages("A48E3A", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 5)
	if !ms[0].TimeFlag || ms[0].TimeNote == "" {
		t.Errorf("%+v", ms[0])
	}
}

// A REP081 report names the flight's origin and destination; the newest one is the current destination.
func TestRouteFromREP081(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "rep081_handoff") // VHHH -> YSSY, report time Oct 29 17:35:51Z
	if _, err := Ingest(st, "780AA4", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	r, ok := st.LastRoute("780AA4")
	if !ok || r.From != "VHHH" || r.To != "YSSY" || !r.T.Equal(time.Date(2026, 10, 29, 17, 35, 51, 0, time.UTC)) {
		t.Fatalf("route = %+v %v", r, ok)
	}
	// Two hours later the same airframe reports the return sector. The newest report wins.
	back := msg(2, f)
	back.Timestamp = f.Received.Add(2 * time.Hour)
	back.Text = airframes.FlexString(strings.NewReplacer("VHHH YSSY", "YSSY VHHH", "2482551", "2489751").Replace(f.Text))
	if _, err := Ingest(st, "780AA4", back); err != nil {
		t.Fatal(err)
	}
	if r, _ = st.LastRoute("780AA4"); r.From != "YSSY" || r.To != "VHHH" || !r.T.Equal(time.Date(2026, 10, 29, 19, 35, 51, 0, time.UTC)) {
		t.Errorf("newer route = %+v", r)
	}
	// Messages that are not REP081 (e.g. Delta's) never give a route.
	st2 := newStore(t)
	Ingest(st2, "A48E3A", msg(1, fixture(t, "dal_fms_posn")))
	Ingest(st2, "A48E3A", msg(2, fixture(t, "cpdlc_wilco")))
	if r, ok := st2.LastRoute("A48E3A"); ok {
		t.Errorf("route from non-REP081: %+v", r)
	}
}

// The flight a message names comes from our own decode of its raw text.
func TestFlightFromMessages(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "adsc_periodic_prefixed") // prefix J25ACX0833: flight CX833, tail B-LRM
	if _, err := Ingest(st, "780AB8", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	got, ok := st.LastFlight("780AB8")
	if !ok || got.Flight != "CX833" || !got.T.Equal(f.Received) {
		t.Fatalf("from prefix = %+v %v", got, ok)
	}
	// An AFN logon names the ICAO callsign.
	a := fixture(t, "afn_contact") // FMHCPA833, B-LRD
	if _, err := Ingest(st, "780AA4", msg(2, a)); err != nil {
		t.Fatal(err)
	}
	if got, ok = st.LastFlight("780AA4"); !ok || got.Callsign != "CPA833" {
		t.Errorf("from AFN = %+v %v", got, ok)
	}
	// A message with no prefix and no AFN (a bare CPDLC) names no flight.
	st2 := newStore(t)
	Ingest(st2, "780AA4", msg(3, fixture(t, "cpdlc_wilco")))
	if got, ok := st2.LastFlight("780AA4"); ok {
		t.Errorf("flight from a bare CPDLC: %+v", got)
	}
}

func TestIngestKeepsWeatherSpeedAndFixes(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "cpdlc_position") // received Oct 28 16:07Z; position 16:06
	if _, err := Ingest(st, "780AB8", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages("780AB8", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 5)
	m := ms[0]
	if m.WxDir == nil || *m.WxDir != 70 || *m.WxKt != 50 || *m.WxTemp != -45 || *m.GS != 520 || *m.Trk != 184 {
		t.Errorf("wx/motion = %v %v %v %v %v", m.WxDir, m.WxKt, m.WxTemp, m.GS, m.Trk)
	}
	var fl store.FixList
	if err := json.Unmarshal([]byte(m.Fixes), &fl); err != nil {
		t.Fatalf("fixes %q: %v", m.Fixes, err)
	}
	day := func(h, mi int) int64 { return time.Date(2026, 10, 28, h, mi, 0, 0, time.UTC).Unix() }
	if len(fl.List) != 3 || fl.List[0].Name != "UNTUL" || fl.List[0].T != day(15, 58) || *fl.List[0].AltFt != 34300 ||
		fl.List[1].Name != "SABNO" || fl.List[1].T != day(16, 7) || fl.List[2].Name != "DALRA" || fl.List[2].T != 0 {
		t.Errorf("fix list = %+v", fl)
	}
	if *fl.GS != 520 || *fl.Trk != 184 {
		t.Errorf("fix list motion = %v %v", fl.GS, fl.Trk)
	}
	// A message with none of this stores none of it.
	w := fixture(t, "cpdlc_wilco")
	Ingest(st, "780AA4", msg(2, w))
	ms, _ = st.Messages("780AA4", w.Received.Add(-time.Hour), w.Received.Add(time.Hour), 5)
	if ms[0].Fixes != "" || ms[0].WxKt != nil || ms[0].GS != nil {
		t.Errorf("wilco: %+v", ms[0])
	}
}
