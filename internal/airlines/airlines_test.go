package airlines

import "testing"

func TestForms(t *testing.T) {
	cases := []struct{ in, icao, iata string }{
		{"CPA113", "CPA113", "CX113"},
		{"CX113", "CPA113", "CX113"},
		{"cx0113", "CPA113", "CX113"}, // zero padding and case
		{"CPA0833", "CPA833", "CX833"},
		{"UAL869", "UAL869", "UA869"},
		{"DAL1317", "DAL1317", "DL1317"},
		{"B6123", "JBU123", "B6123"}, // a designator that contains a digit
		{"BAW9", "BAW9", "BA9"},
		{"ZZ123", "", ""}, // airline we do not know
		{"QQQ123", "", ""},
		{"N12345", "", ""}, // a registration, not a flight
		{"", "", ""},
		{"CPA", "", ""},
	}
	for _, c := range cases {
		if icao, iata := Forms(c.in); icao != c.icao || iata != c.iata {
			t.Errorf("Forms(%q) = %q, %q; want %q, %q", c.in, icao, iata, c.icao, c.iata)
		}
	}
}
