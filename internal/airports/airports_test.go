package airports

import "testing"

func TestLookup(t *testing.T) {
	for _, code := range []string{"SFO", "sfo", "KSFO", " ksfo "} {
		a, ok := Lookup(code)
		if !ok || a.ICAO != "KSFO" || a.IATA != "SFO" || a.Lat < 37.6 || a.Lat > 37.7 || a.Lon > -122.3 {
			t.Errorf("%q: %+v %v", code, a, ok)
		}
	}
	if a, ok := Lookup("HKG"); !ok || a.ICAO != "VHHH" {
		t.Errorf("HKG: %+v %v", a, ok)
	}
	for _, code := range []string{"", "ZZ", "QQQ", "XXXX", "UA869"} {
		if a, ok := Lookup(code); ok {
			t.Errorf("%q should be unknown, got %+v", code, a)
		}
	}
}

func TestElevation(t *testing.T) {
	a, ok := Lookup("KDEN")
	if !ok || !a.HasElev || a.ElevFt != 5431 {
		t.Errorf("Denver = %+v", a)
	}
	if a, _ = Lookup("NRT"); !a.HasElev || a.ElevFt != 141 {
		t.Errorf("Narita = %+v", a)
	}
}
