package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

func setup(t *testing.T, pw string) *httptest.Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	ts := httptest.NewServer((&Server{Store: st, Username: "me", Password: pw, Now: func() time.Time { return now }}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func TestWatchAPI(t *testing.T) {
	ts := setup(t, "")
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"UA869 2026-10-31"}`)
	if code != 201 || !strings.Contains(body, `"status":"waiting"`) || !strings.Contains(body, `"ident":"UA869"`) {
		t.Fatalf("add flight: %d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"B-LRM","until":"2026-11-05T00:00:00Z"}`)
	if code != 201 || !strings.Contains(body, `"end":"2026-11-05T00:00:00Z"`) {
		t.Fatalf("add tail: %d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"UA869 someday"}`)
	if code != 400 || !strings.Contains(body, "YYYY-MM-DD") {
		t.Errorf("bad date: %d %s", code, body)
	}
	code, body = do(t, "GET", ts.URL+"/api/watches", "")
	if code != 200 || strings.Count(body, `"id"`) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ = do(t, "DELETE", ts.URL+"/api/watches/1", ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if _, body = do(t, "GET", ts.URL+"/api/watches", ""); strings.Count(body, `"id"`) != 1 {
		t.Errorf("after delete: %s", body)
	}
	if code, body = do(t, "GET", ts.URL+"/api/track/a1b2c3?hours=6", ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("track: %d %s", code, body)
	}
	if code, body = do(t, "GET", ts.URL+"/", ""); code != 200 || !strings.Contains(body, "leaflet") {
		t.Errorf("index: %d", code)
	}
}

func TestWatchDestinationAPI(t *testing.T) {
	ts := setup(t, "")
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"UA869 2026-10-31 sfo"}`)
	if code != 201 || !strings.Contains(body, `"dest":"KSFO"`) || !strings.Contains(body, "San Francisco") {
		t.Fatalf("%d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"UA870 2026-10-31 ZZZ"}`)
	if code != 400 || !strings.Contains(body, "unknown airport") {
		t.Errorf("unknown airport: %d %s", code, body)
	}
}

func TestMessagesEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	lat, lon := 27.6, 152.6
	pt := now.Add(-time.Hour - 2*time.Second)
	for i, ago := range []time.Duration{3 * time.Hour, time.Hour} { // inserted out of order is fine
		st.AddMessage(store.Message{ID: int64(i + 1), Hex: "780AB8", T: now.Add(-ago), Label: "B6", Text: "t" + string(rune('a'+i)),
			Kind: "adsc", Summary: "ADS-C report", Lat: &lat, Lon: &lon, PosT: &pt, Route: `[{"Lat":26.9,"Lon":150}]`,
			Mismatch: "tail: x"})
	}
	st.AddMessage(store.Message{ID: 9, Hex: "780AB8", T: now.Add(-100 * time.Hour), Label: "SA", Text: "old"}) // outside 48 h
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	code, body := do(t, "GET", ts.URL+"/api/messages/780ab8", "")
	if code != 200 || strings.Count(body, `"raw"`) != 2 || strings.Index(body, `"raw":"ta"`) > strings.Index(body, `"raw":"tb"`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, want := range []string{`"summary":"ADS-C report"`, `"route":[{"Lat":26.9,"Lon":150}]`, `"mismatch":"tail: x"`, `"lat":27.6`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if _, body = do(t, "GET", ts.URL+"/api/messages/780AB8?hours=200", ""); strings.Count(body, `"raw"`) != 3 {
		t.Errorf("hours=200: %s", body)
	}
	if _, body = do(t, "GET", ts.URL+"/api/messages/780AB8?limit=1", ""); strings.Count(body, `"raw"`) != 1 || !strings.Contains(body, `"raw":"tb"`) {
		t.Errorf("limit=1 should keep the newest: %s", body)
	}
}

func TestAirframesEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "airframes.io", LastSeen: time.Unix(1793000000, 0)})
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	code, body := do(t, "GET", ts.URL+"/api/airframes", "")
	if code != 200 || !strings.Contains(body, `"hex":"780AB8"`) || !strings.Contains(body, `"tail":"B-LRM"`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestAuth(t *testing.T) {
	ts := setup(t, "secret")
	if code, _ := do(t, "GET", ts.URL+"/api/watches", ""); code != 401 {
		t.Errorf("no creds: %d", code)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/watches", nil)
	req.SetBasicAuth("me", "secret")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("good creds: %d", resp.StatusCode)
	}
	req.SetBasicAuth("me", "wrong")
	if resp, _ = http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Errorf("bad creds: %d", resp.StatusCode)
	}
}

func TestWatchShowsLastPositionAndType(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	e.Hex = "780AB8"
	st.AddWatch(e)
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "adsb.lol", LastSeen: now})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/watches", "")
	if strings.Contains(body, "last_pos") || !strings.Contains(body, `"type":"A359"`) {
		t.Errorf("before any position: %s", body)
	}

	alt := 38000.0
	st.AddPosition(store.Position{Hex: "780AB8", T: now.Add(-10 * time.Minute), Lat: 40, Lon: -100, AltFt: &alt, Source: "adsb.lol"})
	_, body = do(t, "GET", ts.URL+"/api/watches", "")
	if !strings.Contains(body, `"kind":"ADS-B"`) || !strings.Contains(body, `"t":"2026-10-30T17:50:00Z"`) {
		t.Errorf("ADS-B only: %s", body)
	}

	// A later ADS-C report wins; a later but suspect one is ignored.
	lat, lon := 45.0, -150.0
	pt := now.Add(-3 * time.Minute)
	st.AddMessage(store.Message{ID: 1, Hex: "780AB8", T: now.Add(-2 * time.Minute), Label: "B6", Text: "a", Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &pt})
	bad := now.Add(-time.Minute)
	st.AddMessage(store.Message{ID: 2, Hex: "780AB8", T: now, Label: "BA", Text: "b", Kind: "cpdlc", Lat: &lat, Lon: &lon, PosT: &bad, Suspect: "implies 900 kt"})
	_, body = do(t, "GET", ts.URL+"/api/watches", "")
	if !strings.Contains(body, `"kind":"ADS-C"`) || !strings.Contains(body, `"t":"2026-10-30T17:57:00Z"`) || strings.Contains(body, "CPDLC") {
		t.Errorf("ADS-C later than ADS-B: %s", body)
	}
}

func TestMessagesFlagUndecoded(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	st.AddMessage(store.Message{ID: 1, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "H1", Text: "x1", Kind: "unknown", Summary: "Undecoded H1 message: 239N393DA"})
	st.AddMessage(store.Message{ID: 2, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "MA", Text: "x2", Kind: "miam", Summary: "MIAM (not decoded)"})
	st.AddMessage(store.Message{ID: 3, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "BA", Text: "x3", Kind: "cpdlc", Summary: "CPDLC downlink: WILCO"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	st.AddMessage(store.Message{ID: 4, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "H1", Text: "x4", Kind: "fms_position", Summary: "FMS", TimeNote: "arrived 1 Z", TimeFlag: true})
	_, body := do(t, "GET", ts.URL+"/api/messages/A48E3A", "")
	if strings.Count(body, `"time_unverified":true`) != 1 || !strings.Contains(body, `"time_note":"arrived 1 Z"`) {
		t.Errorf("time fields: %s", body)
	}
	if strings.Count(body, `"undecoded":true`) != 2 {
		t.Errorf("want 2 undecoded: %s", body)
	}
}

func TestTrackCarriesSpeedHeadingAndVerticalRate(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	alt, gs, trk, vr := 35025.0, 480.5, 135.0, -1800.0
	st.AddPosition(store.Position{Hex: "A48E3A", T: now.Add(-time.Minute), Lat: 40, Lon: -100, AltFt: &alt, GS: &gs, Track: &trk, VRate: &vr, Callsign: "DAL1317", Source: "adsb.lol"})
	st.AddPosition(store.Position{Hex: "A48E3A", T: now.Add(-30 * time.Second), Lat: 40.1, Lon: -100, OnGround: true, Source: "adsb.lol"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/track/A48E3A", "")
	for _, want := range []string{`"gs":480.5`, `"track":135`, `"vrate":-1800`, `"alt_ft":35025`, `"on_ground":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("track JSON missing %s: %s", want, body)
		}
	}
}

func TestWatchShowsReportedRoute(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	type rep struct {
		age      time.Duration
		from, to string
	}
	// watches returns /api/watches for an aircraft that has sent the given REP081 reports.
	watches := func(reports ...rep) string {
		st, _ := store.Open(":memory:")
		defer st.Close()
		e, _ := watchlist.Parse("B-LRM", now, time.Time{})
		e.Hex = "780AB8"
		st.AddWatch(e)
		for i, r := range reports {
			at := now.Add(-r.age)
			st.AddMessage(store.Message{ID: int64(i + 1), Hex: "780AB8", T: at, PosT: &at, Label: "MA", Text: fmt.Sprint("t", i), Kind: "miam",
				Decoded: `{"fields":{"rep081":{"Origin":"` + r.from + `","Dest":"` + r.to + `"}}}`})
		}
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
		defer ts.Close()
		_, b := do(t, "GET", ts.URL+"/api/watches", "")
		return b
	}

	if b := watches(); strings.Contains(b, `"route"`) {
		t.Errorf("route shown before any REP081: %s", b)
	}
	// Fresh: shown with airport names, not stale.
	b := watches(rep{12 * time.Minute, "VHHH", "YSSY"})
	for _, want := range []string{`"from":"VHHH"`, `"to":"YSSY"`, `"to_name":"Sydney`, `"from_name":"Hong Kong`, `"source":"REP081"`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(b, `"stale"`) {
		t.Errorf("a 12 minute old report is stale: %s", b)
	}
	// The freshness boundary.
	if b = watches(rep{RouteFreshFor - time.Minute, "VHHH", "EDDM"}); !strings.Contains(b, `"to":"EDDM"`) || strings.Contains(b, `"stale"`) {
		t.Errorf("just inside the window: %s", b)
	}
	if b = watches(rep{RouteFreshFor + time.Minute, "VHHH", "EDDM"}); !strings.Contains(b, `"to":"EDDM"`) || !strings.Contains(b, `"stale":true`) {
		t.Errorf("just outside the window: %s", b)
	}
	// The newest report is the current destination (the aircraft has turned round).
	b = watches(rep{3 * time.Hour, "YSSY", "VHHH"}, rep{40 * time.Minute, "VHHH", "YSSY"})
	if !strings.Contains(b, `"from":"VHHH"`) || !strings.Contains(b, `"to":"YSSY"`) || strings.Contains(b, `"stale"`) {
		t.Errorf("newest should win: %s", b)
	}
	// Only an old one: shown, but flagged as possibly out of date.
	if b = watches(rep{3 * time.Hour, "VHHH", "YSSY"}); !strings.Contains(b, `"to":"YSSY"`) || !strings.Contains(b, `"stale":true`) {
		t.Errorf("3 hour old report: %s", b)
	}
	// Too old to be worth showing at all.
	if b = watches(rep{RouteHideAfter + time.Hour, "VHHH", "YSSY"}); strings.Contains(b, `"route"`) {
		t.Errorf("a day-old report still shown: %s", b)
	}
	// An unknown airport code is still shown, just without a name.
	if b = watches(rep{time.Minute, "VHHH", "ZZZZ"}); !strings.Contains(b, `"to":"ZZZZ"`) || strings.Contains(b, `"to_name"`) {
		t.Errorf("unknown airport: %s", b)
	}
}
