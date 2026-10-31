package adsb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/adsblol_point_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFallbackAndParse(t *testing.T) {
	var paths []string
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, "bad"+r.URL.Path)
		w.WriteHeader(403)
	}))
	defer bad.Close()
	body := sample(t)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, "good"+r.URL.Path)
		w.Write(body)
	}))
	defer good.Close()

	c := NewClient(
		Source{Name: "bad", BaseURL: bad.URL, RegPath: "reg"},
		Source{Name: "good", BaseURL: good.URL, RegPath: "registration"},
	)
	c.MinSpacing = time.Millisecond
	ac, err := c.Reg(context.Background(), "n8538v")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "bad/reg/N8538V,good/registration/N8538V" {
		t.Errorf("paths = %v", paths)
	}
	if len(ac) != 1 || ac[0].Hex != "ABB601" || ac[0].Callsign() != "SWA1674" || ac[0].Source != "good" {
		t.Fatalf("got %+v", ac)
	}
	if !ac[0].HasPosition(time.Minute) || ac[0].AltBaro.Feet == nil || *ac[0].AltBaro.Feet != 35025 {
		t.Errorf("position/alt wrong: %+v", ac[0])
	}
}

func TestEmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ac":[],"msg":"No error","total":0}`))
	}))
	defer srv.Close()
	c := NewClient(Source{Name: "s", BaseURL: srv.URL})
	c.MinSpacing = time.Millisecond
	ac, err := c.Hex(context.Background(), "780AB8")
	if err != nil || len(ac) != 0 {
		t.Fatalf("got %v, %v", ac, err)
	}
}

func TestAllSourcesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	c := NewClient(Source{Name: "a", BaseURL: srv.URL}, Source{Name: "b", BaseURL: srv.URL})
	c.MinSpacing = time.Millisecond
	if _, err := c.Callsign(context.Background(), "UAL869"); err == nil || !strings.Contains(err.Error(), "a: HTTP 500") || !strings.Contains(err.Error(), "b: HTTP 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestAltGround(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ac":[{"hex":"abc123","flight":"UAL869  ","alt_baro":"ground","lat":1,"lon":2,"seen_pos":120}]}`))
	}))
	defer srv.Close()
	c := NewClient(Source{Name: "s", BaseURL: srv.URL})
	c.MinSpacing = time.Millisecond
	ac, err := c.Hex(context.Background(), "abc123")
	if err != nil || len(ac) != 1 {
		t.Fatal(err)
	}
	if !ac[0].AltBaro.OnGround || ac[0].HasPosition(time.Minute) {
		t.Errorf("got %+v", ac[0])
	}
}

func TestRateSpacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ac":[]}`)) }))
	defer srv.Close()
	c := NewClient(Source{Name: "s", BaseURL: srv.URL})
	c.MinSpacing = 100 * time.Millisecond
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.Hex(context.Background(), "abc123"); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 190*time.Millisecond {
		t.Errorf("3 requests took %v, want >= 200ms", d)
	}
}

// twoSources serves a fixed JSON answer per source and counts the requests each receives.
func twoSources(t *testing.T, first, second string) (*Client, *[2]int) {
	t.Helper()
	var n [2]int
	mk := func(i int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n[i]++
			if body == "ERR" {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(body))
		}))
	}
	a, b := mk(0, first), mk(1, second)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	c := NewClient(Source{Name: "one", BaseURL: a.URL, RegPath: "reg"}, Source{Name: "two", BaseURL: b.URL, RegPath: "reg"})
	c.MinSpacing = time.Millisecond
	return c, &n
}

const (
	none     = `{"ac":[]}`
	full     = `{"ac":[{"hex":"a1b2c3","flight":"UAL143  ","r":"N24993","t":"B789","lat":40,"lon":-100,"seen_pos":2}]}`
	noCall   = `{"ac":[{"hex":"a1b2c3","lat":40.1,"lon":-100.1,"seen_pos":0.5}]}` // fresher, but no callsign
	staleBut = `{"ac":[{"hex":"a1b2c3","flight":"UAL143  ","r":"N24993","t":"B789","lat":39,"lon":-99,"seen_pos":30}]}`
)

// The N24993 case: the first source has never heard the aircraft, the second has it.
// A position that is only just usable is worth a second opinion; a fresh complete one is not.
func TestStalePositionAsksTheOtherSource(t *testing.T) {
	c, n := twoSources(t, staleBut, noCall) // 30 s old against 0.5 s old
	ac, _ := c.Hex(context.Background(), "a1b2c3")
	if *n != [2]int{1, 1} || *ac[0].Lat != 40.1 {
		t.Errorf("requests %v, lat %v", *n, *ac[0].Lat)
	}
	c, n = twoSources(t, full, noCall) // 2 s old and complete: no second request
	c.Hex(context.Background(), "a1b2c3")
	if *n != [2]int{1, 0} {
		t.Errorf("requests %v", *n)
	}
}

func TestEmptyFirstSourceFallsThrough(t *testing.T) {
	c, n := twoSources(t, none, full)
	ac, err := c.Reg(context.Background(), "N24993")
	if err != nil || len(ac) != 1 || ac[0].Callsign() != "UAL143" || ac[0].Source != "two" {
		t.Fatalf("got %+v, %v", ac, err)
	}
	if *n != [2]int{1, 1} {
		t.Errorf("requests = %v", *n)
	}
}

func TestCompleteAnswerStopsAtTheFirstSource(t *testing.T) {
	c, n := twoSources(t, full, full)
	ac, _ := c.Reg(context.Background(), "N24993")
	if len(ac) != 1 || ac[0].Source != "one" || *n != [2]int{1, 0} {
		t.Errorf("got %+v, requests %v: a complete answer should not cost a second request", ac, *n)
	}
}

func TestMissingCallsignIsFilledFromAnotherSource(t *testing.T) {
	// The first source has the freshest position but no callsign; the second has the callsign.
	c, n := twoSources(t, noCall, staleBut)
	ac, _ := c.Hex(context.Background(), "a1b2c3")
	if len(ac) != 1 {
		t.Fatalf("got %+v", ac)
	}
	a := ac[0]
	if a.Callsign() != "UAL143" || a.Reg != "N24993" || a.Type != "B789" {
		t.Errorf("identity not filled in: %+v", a)
	}
	if *a.Lat != 40.1 || a.Source != "one" {
		t.Errorf("the fresher position should win: lat %v from %s", *a.Lat, a.Source)
	}
	if *n != [2]int{1, 1} {
		t.Errorf("requests = %v", *n)
	}
	// ... and the other way round: the second source has the fresher position.
	c, _ = twoSources(t, staleBut, noCall)
	ac, _ = c.Hex(context.Background(), "a1b2c3")
	if *ac[0].Lat != 40.1 || ac[0].Source != "two" || ac[0].Callsign() != "UAL143" {
		t.Errorf("reverse: %+v", ac[0])
	}
}

func TestOneSourceErroringIsTolerated(t *testing.T) {
	c, _ := twoSources(t, "ERR", full)
	if ac, err := c.Reg(context.Background(), "N24993"); err != nil || len(ac) != 1 {
		t.Errorf("got %+v, %v", ac, err)
	}
	c, _ = twoSources(t, none, "ERR") // an empty answer plus an error is still an answer
	if ac, err := c.Reg(context.Background(), "N24993"); err != nil || len(ac) != 0 {
		t.Errorf("empty + error: %+v, %v", ac, err)
	}
	c, _ = twoSources(t, "ERR", "ERR")
	if _, err := c.Reg(context.Background(), "N24993"); err == nil {
		t.Error("both failing should be an error")
	}
}

func TestBothEmptyIsEmptyNotError(t *testing.T) {
	c, n := twoSources(t, none, none)
	if ac, err := c.Reg(context.Background(), "N24993"); err != nil || len(ac) != 0 || *n != [2]int{1, 1} {
		t.Errorf("got %+v, %v, requests %v", ac, err, *n)
	}
}

func TestMergeKeepsDistinctAircraft(t *testing.T) {
	c, _ := twoSources(t,
		`{"ac":[{"hex":"aaaaaa","lat":1,"lon":1,"seen_pos":1}]}`, // no callsign yet, so the second source is asked too
		`{"ac":[{"hex":"bbbbbb","flight":"DAL2","lat":2,"lon":2,"seen_pos":1},{"hex":"AAAAAA","flight":"AAL1","lat":1,"lon":1,"seen_pos":3}]}`)
	ac, _ := c.Callsign(context.Background(), "x")
	if len(ac) != 2 || ac[0].Hex != "AAAAAA" || ac[1].Hex != "BBBBBB" {
		t.Fatalf("got %+v", ac)
	}
}

// A source that refuses us (HTTP 403, as airplanes.live does until its operators grant access) is asked once,
// then left alone for an hour; one that says slow down (429) for a while, longer if it says so.
func TestRefusedSourceIsPaused(t *testing.T) {
	for _, tc := range []struct {
		code       int
		retryAfter string
		pause      time.Duration
	}{
		{403, "", time.Hour}, {401, "", time.Hour}, {429, "", 30 * time.Second}, {429, "1200", 20 * time.Minute}, {429, "99999", time.Hour},
	} {
		var refused, answered int
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			refused++
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(tc.code)
		}))
		good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			answered++
			w.Write([]byte(`{"ac":[{"hex":"86d646","flight":"TZP1","lat":35,"lon":140,"seen_pos":1}],"msg":"No error"}`))
		}))
		clock := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
		c := NewClient(Source{Name: "refuses", BaseURL: bad.URL, RegPath: "reg"}, Source{Name: "answers", BaseURL: good.URL, RegPath: "reg"})
		c.MinSpacing = time.Millisecond
		c.Now = func() time.Time { return clock }
		var told []int
		c.OnPause = func(source string, status int, until time.Time) { told = append(told, status) }
		for i := 0; i < 3; i++ {
			if ac, err := c.Reg(context.Background(), "JA826J"); err != nil || len(ac) != 1 {
				t.Fatalf("%d: %v %v", tc.code, ac, err)
			}
		}
		if len(told) != 1 || told[0] != tc.code {
			t.Errorf("HTTP %d: told of the pause %v, want once with the status", tc.code, told)
		}
		if refused != 1 || answered != 3 {
			t.Errorf("HTTP %d (Retry-After %q): refused source asked %d times, other %d; want 1 and 3", tc.code, tc.retryAfter, refused, answered)
		}
		clock = clock.Add(tc.pause - time.Second)
		c.Reg(context.Background(), "JA826J")
		if refused != 1 {
			t.Errorf("HTTP %d: asked again before the pause (%v) was over", tc.code, tc.pause)
		}
		clock = clock.Add(2 * time.Second)
		c.Reg(context.Background(), "JA826J")
		if refused != 2 {
			t.Errorf("HTTP %d: not asked again after the pause", tc.code)
		}
		bad.Close()
		good.Close()
	}
}

func TestAirplanesLiveSource(t *testing.T) {
	if AirplanesLive.BaseURL != "https://api.airplanes.live/v2" || AirplanesLive.RegPath != "reg" {
		t.Errorf("%+v", AirplanesLive) // GET https://api.airplanes.live/v2/reg/{reg}, per its API reference
	}
}
