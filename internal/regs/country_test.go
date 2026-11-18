package regs

import "testing"

func TestCountryForHex(t *testing.T) {
	for _, c := range []struct{ hex, name, code string }{
		{"406B22", "United Kingdom", "GB"}, // G-XLEI
		{"A8D3C1", "United States", "US"},
		{"C0123A", "Canada", "CA"},
		{"7C6B0B", "Australia", "AU"},
		{"780A00", "China", "CN"},
		{"789A12", "Hong Kong", "HK"}, // inside China's block: Cathay's B-L... aircraft
		{"899123", "Taiwan", "TW"},
		{"3C4DD2", "Germany", "DE"},
		{"E49403", "Brazil", "BR"},
		{"8A0001", "Indonesia", "ID"},
		{"06A000", "Qatar", "QA"},
		{"4CA000", "Ireland", "IE"},
		{"1FFFFF", "Russia", "RU"},
	} {
		if n, code, ok := CountryForHex(c.hex); !ok || n != c.name || code != c.code {
			t.Errorf("%s: %q %q %v, want %q %q", c.hex, n, code, ok, c.name, c.code)
		}
	}
	for _, c := range []struct{ hex, tail, name string }{
		{"780AB8", "B-LRM", "Hong Kong"}, // Cathay's A350: Hong Kong registered, in China's block
		{"780AA4", "B-LRD", "Hong Kong"},
		{"781D8D", "B-221W", "China"},
		{"780123", "B-MAA", "Macao"},
		{"780123", "", "China"},
		{"406B22", "G-XLEI", "United Kingdom"},
		{"899124", "B-16783", "Taiwan"},
	} {
		if n, _, ok := CountryFor(c.hex, c.tail); !ok || n != c.name {
			t.Errorf("%s %s: %q, want %q", c.hex, c.tail, n, c.name)
		}
	}
	for _, h := range []string{"", "ZZZZZZ", "123", "F00001", "2FFFFF", "0A9000"} { // unassigned, malformed, or held for ICAO
		if n, _, ok := CountryForHex(h); ok {
			t.Errorf("%q read as %q", h, n)
		}
	}
	// the blocks never run backwards
	for _, b := range countryBlocks {
		if b.end < b.start || len(b.code) != 2 || b.name == "" {
			t.Errorf("block %+v", b)
		}
	}
}
