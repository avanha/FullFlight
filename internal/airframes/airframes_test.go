package airframes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/airframes/tail/B-LRM", "/airframes/icao/780AB8":
			w.Write([]byte(`{"id":20893,"tail":"b-lrm","icao":"780ab8","icaoType":"A359"}`))
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
	if _, err = c.ByTail(context.Background(), "B-ERR"); err == nil {
		t.Error("HTTP 500 should be an error")
	}
}
