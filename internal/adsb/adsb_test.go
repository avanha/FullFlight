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
