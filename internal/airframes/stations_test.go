package airframes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// the shape of airframes.io's /v1/stations answer, trimmed from a real one: EB-LZIB's own entry, and one with no position
const stationsJSON = `[
 {"id":237331075,"uuid":"144bc517","ident":"EB-LZIB","type":"airframes","ipAddress":"147.xxx.xxx.210","sourceApplication":"dumpvdl2","sourceType":"vdl",
  "latitude":48.1486,"longitude":17.10775,"useFuzzedLocation":true,"nearestAirportIcao":"LZIB","countryCode":"SK","countryName":"Slovakia","status":"active",
  "flagged":true,"flaggedReason":"Rate anomaly detected","lastReportAt":"2026-11-01T21:21:46.006Z","messagesCount":235660243,"equipmentSdr":null},
 {"id":1,"ident":"NOWHERE-1","sourceApplication":"acarsdec","sourceType":null,"latitude":null,"longitude":null,"useFuzzedLocation":false,"flagged":false,
  "flaggedReason":null,"lastReportAt":null,"messagesCount":0,"countryCode":null}
]`

func TestStations(t *testing.T) {
	var gotUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		if r.URL.Path != "/v1/stations" {
			t.Errorf("asked for %s", r.URL.Path)
		}
		w.Write([]byte(stationsJSON))
	}))
	defer ts.Close()
	c := NewClient()
	c.BaseURL = ts.URL + "/v1"
	c.UserAgent = "FullFlight/test"
	got, err := c.Stations(context.Background())
	if err != nil || len(got) != 2 || gotUA != "FullFlight/test" {
		t.Fatalf("%+v %v %q", got, err, gotUA)
	}
	e := got[0]
	if e.Ident != "EB-LZIB" || e.Latitude == nil || *e.Latitude != 48.1486 || !e.Fuzzed || e.CountryName != "Slovakia" || e.Application != "dumpvdl2" || e.SourceType != "vdl" ||
		!e.Flagged || e.FlaggedReason != "Rate anomaly detected" || e.MessagesCount != 235660243 || e.NearestAirport != "LZIB" ||
		!e.LastReportAt.Equal(time.Date(2026, 11, 1, 21, 21, 46, 6000000, time.UTC)) {
		t.Errorf("%+v", e)
	}
	if n := got[1]; n.Latitude != nil || n.Flagged || !n.LastReportAt.IsZero() || n.SourceType != "" || n.FlaggedReason != "" {
		t.Errorf("nulls: %+v", n)
	}
}

func TestStationsProblems(t *testing.T) {
	for name, c := range map[string]struct {
		code int
		body string
		want string
	}{
		"rate limited": {429, "", "rate limit"},
		"server error": {503, "", "HTTP 503"},
		"not a list":   {200, `{"message":"changed"}`, "layout may have changed"},
		"empty":        {200, `[]`, "empty"},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.code == 429 {
				w.Header().Set("Retry-After", "30")
			}
			w.WriteHeader(c.code)
			w.Write([]byte(c.body))
		}))
		cl := NewClient()
		cl.BaseURL = ts.URL
		_, err := cl.Stations(context.Background())
		ts.Close()
		var rl *RateLimitError
		if err == nil || !strings.Contains(err.Error(), c.want) || (c.code == 429) != errors.As(err, &rl) || (rl != nil && rl.RetryAfter != 30*time.Second) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
