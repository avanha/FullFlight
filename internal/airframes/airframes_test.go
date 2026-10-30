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

func TestLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/airframes/tail/B-LRM", "/airframes/icao/780AB8":
			w.Write([]byte(`{"id":20893,"tail":"b-lrm","icao":"780ab8","icaoType":"A359"}`))
		case "/airframes/tail/CPA113":
			// airframes.io answers 200 with no body for a registration it does not know.
		case "/airframes/tail/B-ERR":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient()
	c.BaseURL = srv.URL

	a, err := c.ByTail(context.Background(), "b-lrm")
	if err != nil || a == nil || a.ICAO != "780AB8" || a.Tail != "B-LRM" || a.Type != "A359" {
		t.Fatalf("ByTail = %+v, %v", a, err)
	}
	if a, err = c.ByHex(context.Background(), "780ab8"); err != nil || a == nil || a.Tail != "B-LRM" {
		t.Fatalf("ByHex = %+v, %v", a, err)
	}
	if a, err = c.ByTail(context.Background(), "N0NE"); err != nil || a != nil {
		t.Errorf("unknown tail = %+v, %v; want nil, nil", a, err)
	}
	if a, err = c.ByTail(context.Background(), "CPA113"); err != nil || a != nil {
		t.Errorf("empty 200 = %+v, %v; want nil, nil", a, err)
	}
	if _, err = c.ByTail(context.Background(), "B-ERR"); err == nil {
		t.Error("HTTP 500 should be an error")
	}
}

func TestMessages(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		switch r.URL.Query().Get("icao") {
		case "LIMIT":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
		default:
			// blockId/messageNumber arrive as strings, numbers or null; the station embeds a user record we ignore.
			w.Write([]byte(`[{"id":7966522872,"timestamp":"2026-10-30T14:36:04.697Z","label":"MA","blockId":"U","messageNumber":96,
			 "linkDirection":null,"fromHex":"780AA4","toHex":"50","tail":"B-LRD","flightNumber":"CX113","text":"U96ACX0113T-2!<<x",
			 "station":{"ident":"SS-EGBB","user":{"username":"someone"}},"flight":{"flight":"CX0113"},"airframe":{"tail":"B-LRD"},"frequency":null},
			 {"id":2,"timestamp":"2026-10-30T14:00:00Z","label":"SA","text":null}]`))
		}
	}))
	defer srv.Close()
	c := NewClient()
	c.BaseURL = srv.URL
	since := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	ms, err := c.Messages(context.Background(), "780aa4", since, since.Add(24*time.Hour))
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %v", ms, err)
	}
	m := ms[0]
	if m.ID != 7966522872 || m.Label != "MA" || m.MessageNumber != "96" || m.Tail != "B-LRD" || m.Station.Ident != "SS-EGBB" ||
		m.Flight.Flight != "CX0113" || m.LinkDirection != "" || !m.Timestamp.Equal(time.Date(2026, 10, 30, 14, 36, 4, 697000000, time.UTC)) {
		t.Errorf("message = %+v", m)
	}
	if ms[1].Text != "" {
		t.Errorf("null text = %q", ms[1].Text)
	}
	for _, want := range []string{"icao=780AA4", "limit=100", "since=2026-10-30T00%3A00%3A00Z", "until=2026-10-31T00%3A00%3A00Z"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	_, err = c.Messages(context.Background(), "LIMIT", since, since.Add(time.Hour))
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 7*time.Second {
		t.Errorf("err = %v", err)
	}
}
