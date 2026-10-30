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
