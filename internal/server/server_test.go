package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
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
