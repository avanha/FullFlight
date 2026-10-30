package aerodatabox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const sample = `[{"departure":{"airport":{"icao":"KLAX"},"scheduledTime":{"utc":"2026-10-30 17:30Z"},"runwayTime":{"utc":"2026-10-30 17:11Z"}},
"arrival":{"airport":{"icao":"ZSPD"},"scheduledTime":{"utc":"2026-10-31 07:25Z"},"revisedTime":{"utc":"2026-10-31 06:46Z"},"predictedTime":{"utc":"2026-10-31 06:09Z"}},
"number":"CK 624","callSign":"CKK624","status":"EnRoute",
"aircraft":{"reg":"B-221W","modeS":"781d8d","model":"Boeing 777"},
"location":{"groundSpeed":{"kt":466.0},"trueTrack":{"deg":311.0},"reportedAtUtc":"2026-10-30 21:09","lat":58.31338,"lon":-143.82376}}]`

func TestByNumber(t *testing.T) {
	var gotPath, gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("X-RapidAPI-Key")
		if r.URL.Path == "/flights/number/CK624/2026-10-29" {
			w.WriteHeader(204)
			return
		}
		w.Write([]byte(sample))
	}))
	defer ts.Close()
	c := NewClient("secret")
	c.BaseURL = ts.URL
	fs, err := c.ByNumber(context.Background(), "CK624", "2026-10-30")
	if err != nil || len(fs) != 1 {
		t.Fatalf("%v %v", fs, err)
	}
	f := fs[0]
	if gotPath != "/flights/number/CK624/2026-10-30" || gotKey != "secret" {
		t.Errorf("request %s key %q", gotPath, gotKey)
	}
	if f.Hex != "781D8D" || f.Reg != "B-221W" || f.From != "KLAX" || f.To != "ZSPD" || f.Callsign != "CKK624" || !f.Airborne() {
		t.Errorf("%+v", f)
	}
	if !f.Departed.Equal(time.Date(2026, 10, 30, 17, 11, 0, 0, time.UTC)) || !f.Arrives.Equal(time.Date(2026, 10, 31, 6, 9, 0, 0, time.UTC)) {
		t.Errorf("times %v %v", f.Departed, f.Arrives)
	}
	if !f.HasPos || f.Lat != 58.31338 || f.Lon != -143.82376 || !f.PosAt.Equal(time.Date(2026, 10, 30, 21, 9, 0, 0, time.UTC)) || *f.GS != 466 || *f.Track != 311 {
		t.Errorf("position %+v", f)
	}
	if fs, err := c.ByNumber(context.Background(), "CK624", "2026-10-29"); err != nil || len(fs) != 0 {
		t.Errorf("204 should be no flights: %v %v", fs, err)
	}
}

func TestErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer ts.Close()
	c := NewClient("bad")
	c.BaseURL = ts.URL
	if _, err := c.ByNumber(context.Background(), "CK624", "2026-10-30"); err == nil {
		t.Error("401 should be an error")
	}
	// no location (not yet departed) is fine
	fs, err := parse([]byte(`[{"number":"CK 624","status":"Expected","aircraft":{}}]`))
	if err != nil || len(fs) != 1 || fs[0].HasPos || fs[0].Airborne() {
		t.Errorf("%+v %v", fs, err)
	}
}
