package landing

import (
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

var t0 = time.Date(2026, 10, 31, 3, 25, 59, 0, time.UTC)

// JA826J, HNL to NRT: the last ADS-B fix, 400 ft and 1.9 nm from Narita, 147 kt on track 330, then silence.
func narita() Fix {
	return Fix{T: t0, Lat: 35.738, Lon: 140.3946, AltFt: f(400), GS: f(147), Track: f(330), VRate: f(-1024)}
}

func TestLandingAtNarita(t *testing.T) {
	// 47 s to the airport, then a minute's grace for a late fix: not yet.
	if l := Infer(narita(), t0.Add(30*time.Second), ""); l != nil {
		t.Errorf("still on the way down: %+v", l)
	}
	if l := Infer(narita(), t0.Add(90*time.Second), ""); l != nil {
		t.Errorf("inside the grace: %+v", l)
	}
	l := Infer(narita(), t0.Add(3*time.Minute), "")
	if l == nil || l.Airport.ICAO != "RJAA" {
		t.Fatalf("not landed at RJAA: %+v", l)
	}
	if d := l.At.Sub(t0); d < 40*time.Second || d > 55*time.Second {
		t.Errorf("touchdown %v after the fix, want about 47 s", d)
	}
	if l.Basis != "last fix 400 ft (259 above the field), 147 kt, 1.9 nm from RJAA, descending 1024 ft/min, and nothing since" {
		t.Errorf("basis = %q", l.Basis)
	}
	// Knowing where it was going changes nothing here.
	if l := Infer(narita(), t0.Add(3*time.Minute), "NRT"); l == nil || l.Airport.ICAO != "RJAA" {
		t.Errorf("with destination: %+v", l)
	}
}

func TestNotLanding(t *testing.T) {
	later := t0.Add(10 * time.Minute)
	cruise := narita()
	cruise.AltFt = f(35000)
	if Infer(cruise, later, "") != nil {
		t.Error("an aircraft at 35,000 ft over an airport has not landed")
	}
	fast := narita()
	fast.GS = f(320)
	if Infer(fast, later, "") != nil {
		t.Error("320 kt is not an approach")
	}
	away := narita()
	away.Track = f(150) // the airport is behind it
	if Infer(away, later, "") != nil {
		t.Error("heading away from the airport is a departure, not a landing")
	}
	noAlt := narita()
	noAlt.AltFt = nil
	if Infer(noAlt, later, "") != nil {
		t.Error("no altitude, no inference")
	}
	remote := Fix{T: t0, Lat: 30, Lon: -150, AltFt: f(400), GS: f(147), Track: f(330)}
	if Infer(remote, later, "") != nil {
		t.Error("no airport within reach")
	}
}

// Denver's field is at 5,431 ft: an aircraft there is "under 5,000 ft" only above the field, not by the altimeter.
func TestHighAirport(t *testing.T) {
	fix := Fix{T: t0, Lat: 39.90, Lon: -104.67, AltFt: f(5900), GS: f(150), Track: f(180), VRate: f(-700)}
	l := Infer(fix, t0.Add(10*time.Minute), "")
	if l == nil || l.Airport.ICAO != "KDEN" {
		t.Fatalf("Denver: %+v", l)
	}
	fix.AltFt = f(11000) // 5,600 ft above the field: still on the way down from the north, not landing
	if Infer(fix, t0.Add(10*time.Minute), "") != nil {
		t.Error("11,000 ft over Denver is not a landing")
	}
}

func TestOnTheGround(t *testing.T) {
	g := narita()
	g.OnGround, g.AltFt, g.GS = true, nil, f(12)
	l := Infer(g, t0, "")
	if l == nil || l.Airport.ICAO != "RJAA" || !l.At.Equal(t0) {
		t.Errorf("on the ground: %+v", l)
	}
}

func TestDestinationBreaksTheTie(t *testing.T) {
	// North of both San Francisco and Oakland, heading south: Oakland is nearer, but this flight is going to SFO.
	fix := Fix{T: t0, Lat: 37.80, Lon: -122.25, AltFt: f(2500), GS: f(160), Track: f(180), VRate: f(-700)}
	later := t0.Add(10 * time.Minute)
	if l := Infer(fix, later, ""); l == nil || l.Airport.ICAO != "KOAK" {
		t.Errorf("no destination: %+v", l)
	}
	if l := Infer(fix, later, "KSFO"); l == nil || l.Airport.ICAO != "KSFO" {
		t.Errorf("destination KSFO: %+v", l)
	}
}
