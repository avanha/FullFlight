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

func TestSearch(t *testing.T) {
	first := func(q string) string {
		got, _ := Search(q, 10)
		if len(got) == 0 {
			return ""
		}
		return got[0].ICAO
	}
	for q, want := range map[string]string{"nrt": "RJAA", "RJAA": "RJAA", "narita": "RJAA", "ksfo": "KSFO", "sfo": "KSFO", "hong kong": "VHHH"} {
		if got := first(q); got != want {
			t.Errorf("Search(%q) first = %q, want %q", q, got, want)
		}
	}
	// An exact code comes first (KDE is Koroba's IATA code), then codes that start with the text.
	got, _ := Search("kde", 10)
	if len(got) < 3 || got[0].ICAO != "AYOW" || got[1].ICAO[:3] != "KDE" {
		t.Errorf("kde: %+v", got)
	}
	if got, more := Search("international", 5); len(got) != 5 || !more {
		t.Errorf("limit: %d more %v", len(got), more)
	}
	if got, _ := Search("zzzzqq", 5); got != nil {
		t.Errorf("no match = %+v", got)
	}
	if got, _ := Search("  ", 5); got != nil {
		t.Errorf("blank = %+v", got)
	}
}
