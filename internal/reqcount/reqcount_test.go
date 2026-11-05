package reqcount

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fail struct{}

func (fail) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("no route") }

func TestCountsByHostAndOutcome(t *testing.T) {
	status := 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer ts.Close()
	type seen struct {
		host   string
		status int
		failed bool
	}
	var got []seen
	c := &http.Client{}
	rec := func(h string, s int, f bool) { got = append(got, seen{h, s, f}) }
	Wrap(c, rec)
	Wrap(c, rec) // once only
	host := ts.Listener.Addr().String()
	for _, s := range []int{200, 429, 503} {
		status = s
		resp, err := c.Get(ts.URL + "/x")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	want := []seen{{host, 200, false}, {host, 429, true}, {host, 503, true}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
	got = nil
	c2 := &http.Client{Transport: fail{}}
	Wrap(c2, rec)
	if _, err := c2.Get("http://nowhere.example/x"); err == nil {
		t.Fatal("no error")
	}
	if len(got) != 1 || got[0] != (seen{"nowhere.example", 0, true}) {
		t.Errorf("a request that got no answer: %v", got)
	}
}
