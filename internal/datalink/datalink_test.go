package datalink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
	if m := by[3]; m.Kind != "unknown" || !strings.Contains(m.Summary, "not decoded") {
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

// fmsMsg is an FMS report from a healthy station: its waypoint time is the arrival time.
func fmsMsg(id int64, at time.Time, text string) airframes.Message {
	text = strings.Replace(text, ",WPT,000000,", ",WPT,"+at.Format("150405")+",", 1)
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

// A position whose own time is hours from its arrival time is not judged against where the other fixes were at
// the arrival time: it is a real position at an unknown time (a station that stamps late), so it is called
// unverified, not impossible. Healthy stations are judged as before.
func TestMisdatedFMSIsUnverifiedNotImpossible(t *testing.T) {
	st := newStore(t)
	t0 := time.Date(2026, 10, 28, 21, 17, 0, 0, time.UTC)
	Ingest(st, "A48E3A", fmsMsg(1, t0, posn(34, 9.8, 115, 41.8)))
	Ingest(st, "A48E3A", fmsMsg(2, t0.Add(2*time.Minute), posn(34, 8.8, 115, 50.0)))
	// Boston, 2 minutes later, but its waypoint time says it was sent 4 hours earlier
	late := fmsMsg(3, t0.Add(4*time.Minute), posn(41, 28.9, 72, 12.2))
	late.Text = airframes.FlexString(strings.Replace(string(late.Text), ","+t0.Add(4*time.Minute).Format("150405")+",", ",172100,", 1))
	Ingest(st, "A48E3A", late)
	// and the same place from a healthy station is still impossible
	Ingest(st, "A48E3A", fmsMsg(4, t0.Add(5*time.Minute), posn(41, 28.9, 72, 12.2)))
	check := func() {
		t.Helper()
		ms, _ := st.Messages("A48E3A", t0.Add(-time.Hour), t0.Add(2*time.Hour), 20)
		for _, m := range ms {
			switch m.ID {
			case 3:
				if m.Suspect != "" || !m.TimeFlag {
					t.Errorf("misdated: suspect=%q timeflag=%v", m.Suspect, m.TimeFlag)
				}
			case 4:
				if m.Suspect == "" || m.TimeFlag {
					t.Errorf("healthy but impossible: suspect=%q timeflag=%v", m.Suspect, m.TimeFlag)
				}
			}
		}
	}
	check()
	if _, err := Recheck(st, "A48E3A", t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	check()
	// an old flag on such a message is cleared by a recheck
	st.SetSuspect(3, "left over from before")
	Recheck(st, "A48E3A", t0.Add(-time.Hour))
	check()
}

// A flight plan names the flight (and so labels messages that were undecoded); a progress report filed days
// after it was written does not get to name the flight the aircraft is on now.
func TestPlanAndProgressNameTheFlight(t *testing.T) {
	st := newStore(t)
	t0 := time.Date(2026, 10, 30, 18, 47, 0, 0, time.UTC)
	fpn := airframes.Message{ID: 1, Timestamp: t0, Label: "H1", FromHex: "A762CC",
		Text: "FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..N25000W170000..TUNTO.Q13..PICHU,N21557E120506FA3A"}
	late := airframes.Message{ID: 2, Timestamp: t0.Add(72 * time.Minute), Label: "H1", FromHex: "A762CC",
		Text: "PRG/TS040501,281026/DTKONT,26L,400,043500,041/FNUPS57003B"} // written two days earlier
	ontime := airframes.Message{ID: 3, Timestamp: t0.Add(73 * time.Minute), Label: "H1", FromHex: "A762CC",
		Text: "PRG/TS195622,301026/DTVHHH,07R,408,064946,048/FNUPS58FA3A"}
	ontime.Timestamp = time.Date(2026, 10, 30, 19, 56, 30, 0, time.UTC)
	for _, m := range []airframes.Message{fpn, late, ontime} {
		if _, err := Ingest(st, "A762CC", m); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := st.Messages("A762CC", t0.Add(-time.Hour), t0.Add(3*time.Hour), 10)
	by := map[int64]store.Message{}
	for _, m := range ms {
		by[m.ID] = m
	}
	if m := by[1]; m.Kind != "flight_plan" || m.Callsign != "UPS58" || m.Flight != "5X58" || !strings.Contains(m.Summary, "PHNL") {
		t.Errorf("plan: %+v", m)
	}
	if m := by[2]; m.Kind != "progress" || m.Flight != "" || m.Callsign != "" || m.TimeNote == "" {
		t.Errorf("late progress report must not name the flight: %+v", m)
	}
	if m := by[3]; m.Kind != "progress" || m.Callsign != "UPS58" || !strings.Contains(m.Summary, "VHHH") {
		t.Errorf("on-time progress report: %+v", m)
	}
}

type fakeSearch struct {
	msgs  []airframes.Message
	by    map[string][]airframes.Message // results for a given query, when set
	calls []string
}

func (s *fakeSearch) SearchText(_ context.Context, q string) ([]airframes.Message, error) {
	s.calls = append(s.calls, q)
	if s.by != nil {
		return s.by[q], nil
	}
	return s.msgs, nil
}

// Flight plans carry no hex address, so the by-address fetch cannot return them: they are searched for by the
// callsign the aircraft is known to be flying, and only this aircraft's are kept.
func TestFlightPlansFoundByCallsign(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("hex:A762CC", now, time.Time{})
	st.AddWatch(e)
	mine := airframes.Message{ID: 10, Timestamp: now.Add(-3 * time.Hour), Label: "H1",
		Text: "FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..TUNTO..IGURU.Q13..PICHU6CDE"}
	mine.Airframe.ICAO = "A762CC"
	other := airframes.Message{ID: 11, Timestamp: now.Add(-3 * time.Hour), Label: "H1",
		Text: "FPN/FNUPS58/RI:DA:KONT:AA:PHNL:F:LILIA..TUNTO6CDE"} // yesterday's UPS58, another aircraft
	other.Airframe.ICAO = "A11111"
	// the aircraft's recent message names its callsign
	st.AddMessage(store.Message{ID: 1, Hex: "A762CC", T: now.Add(-time.Hour), Label: "H1", Text: "x", Callsign: "UPS58"})
	se := &fakeSearch{msgs: []airframes.Message{mine, other}}
	f := &Fetcher{Store: st, Source: &fakeSource{}, Search: se, Now: func() time.Time { return now }}

	f.Once(context.Background())
	if len(se.calls) != 1 || se.calls[0] != "FPN/FNUPS58" {
		t.Fatalf("searches %v", se.calls)
	}
	ms, _ := st.Messages("A762CC", now.Add(-6*time.Hour), now, 20)
	var plans []store.Message
	for _, m := range ms {
		if m.Kind == "flight_plan" {
			plans = append(plans, m)
		}
	}
	if len(plans) != 1 || plans[0].ID != 10 || plans[0].Callsign != "UPS58" {
		t.Fatalf("plans: %+v", plans)
	}
	// throttled
	now = now.Add(time.Minute)
	f.last = map[string]time.Time{}
	f.Once(context.Background())
	if len(se.calls) != 1 {
		t.Errorf("searched again after a minute: %v", se.calls)
	}
	now = now.Add(16 * time.Minute)
	f.last = map[string]time.Time{}
	f.Once(context.Background())
	if len(se.calls) != 2 {
		t.Errorf("not searched after 16 minutes: %v", se.calls)
	}
}

// Nothing tells a hex watch which flight its aircraft is on, if the messages that say so carry no hex address.
// A search for the registration finds a logon that names the flight; that flight's plan is then searched for in
// the same round.
func TestRegistrationSearchFindsTheFlightThenThePlan(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("hex:A762CC", now, time.Time{})
	st.AddWatch(e)
	st.RecordAirframe(store.Airframe{Hex: "A762CC", Tail: "N575UP", Source: "x", LastSeen: now})
	logon := airframes.Message{ID: 20, Timestamp: now.Add(-2 * time.Hour), Label: "H1", Text: "/OAKODYA.AFN/FMHUPS58,.N575UP,,200552/FAK0,KZAK"}
	logon.Airframe.ICAO = "A762CC"
	plan := airframes.Message{ID: 21, Timestamp: now.Add(-3 * time.Hour), Label: "H1", Text: "FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..TUNTO6CDE"}
	plan.Airframe.ICAO = "A762CC"
	se := &fakeSearch{by: map[string][]airframes.Message{"N575UP": {logon}, "FPN/FNUPS58": {plan}}}
	f := &Fetcher{Store: st, Source: &fakeSource{}, Search: se, Now: func() time.Time { return now }}
	f.Once(context.Background())
	if !reflect.DeepEqual(se.calls, []string{"N575UP", "FPN/FNUPS58"}) {
		t.Fatalf("searches %v", se.calls)
	}
	ms, _ := st.Messages("A762CC", now.Add(-6*time.Hour), now, 20)
	kinds := map[string]bool{}
	for _, m := range ms {
		kinds[m.Kind] = true
	}
	if !kinds["afn"] || !kinds["flight_plan"] {
		t.Errorf("stored kinds %v", kinds)
	}
}

// A *POS report carries its own date: delivered days late, it is placed where and when it was, with a note.
func TestPosReportIsPlacedAtItsOwnTime(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "pos_late") // received 31 Oct 05:02:58, dated 28 Oct 10:29
	if _, err := Ingest(st, "A762CC", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	// (listed with the time it was made, not the day it arrived)
	ms, _ := st.Messages("A762CC", time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), 5)
	if len(ms) != 1 {
		t.Fatalf("listed at the time it was made: %d messages", len(ms))
	}
	if again, _ := st.Messages("A762CC", f.Received.Add(-time.Hour), f.Received.Add(time.Hour), 5); len(again) != 0 {
		t.Errorf("and not among the messages of the day it arrived: %d", len(again))
	}
	m := ms[0]
	if m.Kind != "pos_report" || m.PosT == nil || !m.PosT.Equal(time.Date(2026, 10, 28, 10, 29, 0, 0, time.UTC)) {
		t.Fatalf("kind %q posT %v", m.Kind, m.PosT)
	}
	if m.TimeFlag || !strings.Contains(m.TimeNote, "66h") || !strings.Contains(m.TimeNote, "2026-10-28 10:29Z") {
		t.Errorf("flag=%v note=%q", m.TimeFlag, m.TimeNote)
	}
	// Placed three days ago, it is not the aircraft's newest position by any reading of the clock.
	if p, ok := st.LastMessagePosition("A762CC"); !ok || p.T.After(f.Received.Add(-48*time.Hour)) {
		t.Errorf("last position = %+v", p)
	}
	// Delivered on time it carries no note.
	st2 := newStore(t)
	g := fixture(t, "pos_ontime")
	Ingest(st2, "A762CC", msg(1, g))
	ms, _ = st2.Messages("A762CC", g.Received.Add(-time.Hour), g.Received.Add(time.Hour), 5)
	if ms[0].TimeNote != "" || ms[0].TimeFlag || !ms[0].PosT.Equal(time.Date(2026, 10, 30, 10, 14, 0, 0, time.UTC)) {
		t.Errorf("on time: %q %v %v", ms[0].TimeNote, ms[0].TimeFlag, ms[0].PosT)
	}
}

func TestMarkLate(t *testing.T) {
	st := newStore(t)
	day := func(d, h, m, s int) time.Time { return time.Date(2026, 10, d, h, m, s, 0, time.UTC) }
	lat, lon := 39.1, -123.17
	add := func(id int64, at time.Time, station, kind string, own *time.Time) {
		m := store.Message{ID: id, Hex: "A762CC", T: at, Label: "H1", Text: fmt.Sprint("m", id), Kind: kind, Station: station}
		if own != nil {
			m.PosT, m.Lat, m.Lon = own, &lat, &lon
		}
		if _, err := st.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	own := func(d, h, m int) *time.Time { v := day(d, h, m, 0); return &v }
	// A backlog from 28 Oct delivered through KSFO3 on 31 Oct, 66 hours late, with ADS-C reports in among it.
	add(1, day(31, 4, 42, 58), "JH-KSFO3", "pos_report", own(28, 10, 20))
	add(2, day(31, 4, 59, 37), "JH-KSFO3", "adsc", nil) // 17 minutes on: the same backlog
	add(3, day(31, 5, 2, 58), "JH-KSFO3", "pos_report", own(28, 10, 29))
	add(4, day(31, 4, 50, 0), "SCH-KRDD-VDLM2", "adsc", nil) // another station: judged by nobody
	add(5, day(31, 6, 40, 0), "JH-KSFO3", "adsc", nil)       // too far from any dated message
	add(6, day(31, 4, 55, 55), "JH-KSFO3", "afn", nil)       // any kind, not only positions
	add(7, day(31, 4, 51, 0), "JH-KSFO3", "fms_position", nil)
	// Live traffic: a dated message on time, and an ADS-C report beside it.
	add(8, day(30, 10, 22, 39), "Fa-KSNA-VDL", "pos_report", own(30, 10, 22))
	add(9, day(30, 10, 21, 47), "Fa-KSNA-VDL", "adsc", nil)
	// The nearest dated message decides: here it is the on-time one, though a late one is also within the window.
	add(10, day(30, 10, 20, 0), "JH-KSFO3", "pos_report", own(27, 1, 0))
	add(11, day(30, 10, 36, 0), "JH-KSFO3", "pos_report", own(30, 10, 35))
	add(12, day(30, 10, 34, 0), "JH-KSFO3", "adsc", nil)

	n, err := MarkLate(st, "A762CC", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]store.Message{}
	all, _ := st.Messages("A762CC", day(1, 0, 0, 0), day(31, 23, 0, 0), 100)
	for _, m := range all {
		got[m.ID] = m
	}
	flagged := func(id int64) bool { return got[id].TimeFlag }
	for id, want := range map[int64]bool{1: false, 2: true, 3: false, 4: false, 5: false, 6: true, 7: false, 8: false, 9: false, 10: false, 11: false, 12: false} {
		if flagged(id) != want {
			t.Errorf("message %d flagged = %v, want %v (%q)", id, flagged(id), want, got[id].TimeNote)
		}
	}
	if n != 2 {
		t.Errorf("changed %d, want 2", n)
	}
	if note := got[2].TimeNote; !strings.HasPrefix(note, lateBatchPrefix) || !strings.Contains(note, "2026-10-28") || !strings.Contains(note, "JH-KSFO3") {
		t.Errorf("note = %q", note)
	}
	// Again over the same messages: nothing changes.
	if n, _ := MarkLate(st, "A762CC", time.Time{}); n != 0 {
		t.Errorf("second pass changed %d", n)
	}
}

// DAL39's POSRPT from Point Conception arrived at 01:10 on the 31st carrying 19:41: it is placed at 19:41 on the 30th.
func TestPOSRPTIsPlacedAtItsOwnTime(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "posrpt_dal39_late")
	if _, err := Ingest(st, "A63DFF", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Messages("A63DFF", time.Date(2026, 10, 30, 19, 0, 0, 0, time.UTC), time.Date(2026, 10, 30, 20, 30, 0, 0, time.UTC), 5) // (listed where it was made)
	m := ms[0]
	if m.Kind != "posrpt" || m.PosT == nil || !m.PosT.Equal(time.Date(2026, 10, 30, 19, 41, 0, 0, time.UTC)) || m.Lat == nil {
		t.Fatalf("kind %q posT %v", m.Kind, m.PosT)
	}
	if m.TimeFlag || !strings.Contains(m.TimeNote, "5h 29m") {
		t.Errorf("flag=%v note=%q", m.TimeFlag, m.TimeNote)
	}
	if m.Tail != "N501DN" {
		t.Errorf("tail = %q", m.Tail)
	}
	// Sent on time (the station stamps three minutes early) there is no note.
	g := fixture(t, "posrpt_prefixed") // 13:03:34, TME 13:06
	Ingest(st, "A6745A", msg(2, g))
	ms, _ = st.Messages("A6745A", g.Received.Add(-time.Hour), g.Received.Add(time.Hour), 5)
	if ms[0].TimeNote != "" || ms[0].TimeFlag {
		t.Errorf("on time: %q %v", ms[0].TimeNote, ms[0].TimeFlag)
	}
}

// The other A350 reports name the route and the flight too: a destination and a flight number for an aircraft that
// has sent no REP081 and no ADS-B.
func TestRouteAndFlightFromA350Reports(t *testing.T) {
	st := newStore(t)
	f := fixture(t, "real_a350_rep216") // SIA23 KJFK -> WSSS
	if _, err := Ingest(st, "76CCE3", msg(1, f)); err != nil {
		t.Fatal(err)
	}
	r, ok := st.LastRoute("76CCE3")
	if !ok || r.From != "KJFK" || r.To != "WSSS" || r.Source != "A350 report" {
		t.Fatalf("route = %+v %v", r, ok)
	}
	if fl, ok := st.LastFlight("76CCE3"); !ok || fl.Callsign != "SIA23" {
		t.Errorf("flight = %+v %v", fl, ok)
	}
	// the XML maintenance report does the same
	st2 := newStore(t)
	if _, err := Ingest(st2, "C880A8", msg(1, fixture(t, "real_a350_rtr_xml"))); err != nil {
		t.Fatal(err)
	}
	if r, ok := st2.LastRoute("C880A8"); !ok || r.From != "YSSY" || r.To != "NFFN" {
		t.Errorf("rtr route = %+v %v", r, ok)
	}
}

// A departure report names the flight's route too.
func TestRouteFromDepartureReport(t *testing.T) {
	st := newStore(t)
	if _, err := Ingest(st, "A63DFF", msg(1, fixture(t, "real_delta_departure_miam"))); err != nil {
		t.Fatal(err)
	}
	if r, ok := st.LastRoute("A63DFF"); !ok || r.From != "KLAX" || r.To != "VHHH" || r.Source != "airline report" {
		t.Errorf("route = %+v %v", r, ok)
	}
}

// The top of descent report names the route: the airports as IATA codes, kept as ICAO like the other route reports.
func TestRouteFromTopOfDescent(t *testing.T) {
	st := newStore(t)
	if _, err := Ingest(st, "A762CC", msg(1, fixture(t, "real_ups_tod_hkg_anc"))); err != nil {
		t.Fatal(err)
	}
	if r, ok := st.LastRoute("A762CC"); !ok || r.From != "VHHH" || r.To != "PANC" || r.Source != "airline report" {
		t.Errorf("route = %+v %v", r, ok)
	}
}

func TestSentLate(t *testing.T) {
	arr := func(day, h, m int) time.Time {
		return time.Date(2026, 11, 1, h, m, 0, 0, time.UTC).AddDate(0, 0, day-1)
	}
	for _, c := range []struct {
		name    string
		arrived time.Time
		sent    string
		flag    bool
	}{
		{"on time", arr(1, 1, 11), "010111", false},
		{"a few minutes behind", arr(1, 1, 15), "010111", false},
		{"across midnight", time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC), "312358", false},
		{"across a month end", time.Date(2026, 12, 1, 0, 2, 0, 0, time.UTC), "302359", false},
		{"a day late", time.Date(2026, 12, 1, 0, 2, 0, 0, time.UTC), "292359", true},
		{"three days late", arr(1, 2, 57), "280937", true},
		{"hours late", arr(1, 6, 0), "010111", true},
		{"nonsense", arr(1, 1, 11), "019999", false},
	} {
		note, flag := sentLate(c.arrived, c.sent)
		if flag != c.flag {
			t.Errorf("%s: flag %v, want %v (%s)", c.name, flag, c.flag, note)
		}
		if flag && !strings.Contains(note, "not applied") {
			t.Errorf("%s: note %q", c.name, note)
		}
	}
}
