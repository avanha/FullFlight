package watchlist

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func TestParseFlight(t *testing.T) {
	e, err := Parse("ua869 2026-10-31", now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindFlight || e.Ident != "UA869" || e.Status != StatusWaiting {
		t.Fatalf("got %+v", e)
	}
	// 12 h before 2026-10-31 00:00Z is 2026-10-30 12:00Z, already past, so start clamps to now.
	if !e.Start.Equal(now) {
		t.Errorf("start = %v, want now", e.Start)
	}
	if want := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC); !e.End.Equal(want) {
		t.Errorf("end = %v, want %v", e.End, want)
	}
}

func TestParseFlightFuture(t *testing.T) {
	e, err := Parse("UA869 2026-11-05", now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC); !e.Start.Equal(want) {
		t.Errorf("start = %v, want %v", e.Start, want)
	}
	if e.Active(now) {
		t.Error("entry should not be active before its start")
	}
}

func TestParseAircraft(t *testing.T) {
	cases := []struct {
		in   string
		kind Kind
	}{
		{"B-LRM", KindTail},
		{"N8538V", KindTail},
		{"780AB8", KindHex},
		{"abb601", KindHex},
	}
	for _, c := range cases {
		e, err := Parse(c.in, now, time.Time{})
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if e.Kind != c.kind || e.Status != StatusTracking {
			t.Errorf("%s: got %+v", c.in, e)
		}
		if !e.End.Equal(now.Add(36 * time.Hour)) {
			t.Errorf("%s: default end = %v, want now+36h", c.in, e.End)
		}
	}
}

func TestParseUntil(t *testing.T) {
	until := now.Add(72 * time.Hour)
	e, err := Parse("B-LRM", now, until)
	if err != nil || !e.End.Equal(until) {
		t.Fatalf("got %+v, %v", e, err)
	}
	if _, err := Parse("B-LRM", now, now.Add(-time.Hour)); err == nil {
		t.Error("past end should be rejected")
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "UA869 tomorrow", "UA869 2026-10-01", "!!", "B-LRM 2026-10-31 x"} {
		if _, err := Parse(in, now, time.Time{}); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestExpiry(t *testing.T) {
	e, _ := Parse("B-LRM", now, time.Time{})
	if !e.Active(now.Add(35*time.Hour)) || e.Expired(now.Add(35*time.Hour)) {
		t.Error("should be active at 35h")
	}
	if e.Active(now.Add(36*time.Hour)) || !e.Expired(now.Add(36*time.Hour)) {
		t.Error("should be expired at 36h")
	}
}
