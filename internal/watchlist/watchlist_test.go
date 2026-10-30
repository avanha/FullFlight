package watchlist

import (
	"strings"
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

func TestParseDestination(t *testing.T) {
	for in, want := range map[string]string{"UA869 2026-11-05 sfo": "SFO", "UA869 2026-11-05 KSFO": "KSFO"} {
		e, err := Parse(in, now, time.Time{})
		if err != nil || e.Dest != want || e.Kind != KindFlight {
			t.Errorf("%q: %+v %v", in, e, err)
		}
	}
	e, _ := Parse("UA869 2026-11-05", now, time.Time{})
	if e.Dest != "" {
		t.Errorf("destination should be optional, got %q", e.Dest)
	}
	for _, in := range []string{"UA869 2026-11-05 SF", "UA869 2026-11-05 S-FO", "UA869 2026-11-05 SFO LAX", "B-LRM SFO"} {
		if _, err := Parse(in, now, time.Time{}); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

// Bare callsigns and flight numbers are flights, not registrations. A bare
// "CPA113" was once filed as a tail because the tail pattern is very loose.
func TestParseBareFlight(t *testing.T) {
	for in, ident := range map[string]string{"CPA113": "CPA113", "cpa113": "CPA113", "UAL0869": "UAL0869", "UA869": "UA869", "ACA123": "ACA123", "BAW9": "BAW9"} {
		e, err := Parse(in, now, time.Time{})
		if err != nil || e.Kind != KindFlight || e.Ident != ident || e.Date != "" || e.Status != StatusWaiting {
			t.Errorf("%q: %+v %v", in, e, err)
			continue
		}
		if !e.Start.Equal(now) || !e.End.Equal(now.Add(36*time.Hour)) {
			t.Errorf("%q: window %v..%v", in, e.Start, e.End)
		}
	}
	e, err := Parse("CPA113 sfo", now, now.Add(48*time.Hour))
	if err != nil || e.Kind != KindFlight || e.Dest != "SFO" || !e.End.Equal(now.Add(48*time.Hour)) {
		t.Errorf("with destination and until: %+v %v", e, err)
	}
	if _, err := Parse("CPA113 S-FO", now, time.Time{}); err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Error("bad destination accepted")
	}
	// A dated flight still works, and registrations that look a bit like flights stay tails.
	if e, _ := Parse("CPA113 2026-11-05", now, time.Time{}); e.Date != "2026-11-05" {
		t.Errorf("dated: %+v", e)
	}
	for in, kind := range map[string]Kind{"N12345": KindTail, "N8538V": KindTail, "JA123A": KindTail, "HL7500": KindTail,
		"B-LRM": KindTail, "VH-OQA": KindTail, "780AB8": KindHex, "A6F9B3": KindHex} {
		if e, err := Parse(in, now, time.Time{}); err != nil || e.Kind != kind {
			t.Errorf("%q: kind %v, want %v (%v)", in, e.Kind, kind, err)
		}
	}
}

// Callsigns of airlines nobody told us about are still callsigns: three letters then digits is not a
// registration. TZP1 (ZIPAIR) and CKK624 (China Cargo) were once looked up as tail numbers.
func TestUnknownAirlineCallsigns(t *testing.T) {
	for _, in := range []string{"TZP1", "CKK624", "tzp1", "QXZ9999", "QBC12D"} {
		e, err := Parse(in, now, time.Time{})
		if err != nil || e.Kind != KindFlight || e.Ident != strings.ToUpper(in) || e.Date != "" || e.Status != StatusWaiting {
			t.Errorf("%q: %+v %v", in, e, err)
		}
	}
	// with a destination and no date, and with a date
	if e, err := Parse("CKK624 PANC", now, time.Time{}); err != nil || e.Kind != KindFlight || e.Dest != "PANC" || e.Date != "" {
		t.Errorf("destination: %+v %v", e, err)
	}
	if e, err := Parse("TZP1 2026-11-05", now, time.Time{}); err != nil || e.Kind != KindFlight || e.Date != "2026-11-05" {
		t.Errorf("dated: %+v %v", e, err)
	}
	// registrations that merely have digits in them are still registrations
	for in, kind := range map[string]Kind{"N12345": KindTail, "JA123A": KindTail, "HL7500": KindTail, "B-LRM": KindTail, "VH-OQA": KindTail,
		"N8538V": KindTail, "DAIMA": KindTail, "780AB8": KindHex, "A6F9B3": KindHex} {
		if e, err := Parse(in, now, time.Time{}); err != nil || e.Kind != kind {
			t.Errorf("%q: kind %v, want %v (%v)", in, e.Kind, kind, err)
		}
	}
}

// The one real clash: six hex digits that also read as a callsign. A known airline makes it a callsign,
// otherwise it is an address (and flight: or hex: settles it either way).
func TestHexShapedCallsign(t *testing.T) {
	cases := map[string]Kind{"ACA123": KindFlight, "AFR456": KindFlight, "AEE123": KindHex, "ABC123": KindHex,
		"flight:AEE123": KindFlight, "callsign:abc123": KindFlight, "hex:ACA123": KindHex, "tail:AEE123": KindTail}
	for in, kind := range cases {
		if e, err := Parse(in, now, time.Time{}); err != nil || e.Kind != kind {
			t.Errorf("%q: kind %v, want %v (%v)", in, e.Kind, kind, err)
		}
	}
	if e, _ := Parse("hex:abc123", now, time.Time{}); e.Hex != "ABC123" || e.Ident != "ABC123" {
		t.Errorf("hex entry = %+v", e)
	}
}

// A two-letter airline we do not know is taken for a registration, as JA123A must be; flight: says otherwise.
func TestExplicitPrefixes(t *testing.T) {
	if e, _ := Parse("XQ624", now, time.Time{}); e.Kind != KindTail {
		t.Errorf("XQ624 alone = %+v", e)
	}
	for in, want := range map[string]struct {
		kind      Kind
		ident, dt string
		dest      string
	}{
		"flight:XQ624":                 {KindFlight, "XQ624", "", ""},
		"FLIGHT:xq624 2026-11-05":      {KindFlight, "XQ624", "2026-11-05", ""},
		"flight:XQ624 PANC":            {KindFlight, "XQ624", "", "PANC"},
		"flight:XQ624 2026-11-05 PANC": {KindFlight, "XQ624", "2026-11-05", "PANC"},
		"tail:CKK624":                  {KindTail, "CKK624", "", ""},
		"reg:b-lrm":                    {KindTail, "B-LRM", "", ""},
	} {
		e, err := Parse(in, now, time.Time{})
		if err != nil || e.Kind != want.kind || e.Ident != want.ident || e.Date != want.dt || e.Dest != want.dest {
			t.Errorf("%q: %+v %v", in, e, err)
		}
	}
	for _, in := range []string{"hex:TZP1", "hex:12345", "hex:", "foo:bar", "tail:", "tail:B-LRM 2026-11-05", "hex:ABC123 SFO", "tail:!!!", "flight:"} {
		if e, err := Parse(in, now, time.Time{}); err == nil {
			t.Errorf("%q should be rejected, got %+v", in, e)
		}
	}
}
