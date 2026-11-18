package regs

import (
	"strconv"
	"strings"
)

type countryBlock struct {
	start, end int
	name, code string // the state, and its two-letter ISO 3166 code
}

// CountryForHex returns the state an ICAO address was allocated to, which is the aircraft's country of registration, and its
// ISO 3166 two-letter code. The address is six hex digits in either case. Where blocks overlap (Hong Kong inside China) the smaller wins.
func CountryForHex(hexAddr string) (name, code string, ok bool) {
	n, err := strconv.ParseInt(hexAddr, 16, 32)
	if err != nil || len(hexAddr) != 6 {
		return "", "", false
	}
	best := -1
	for i, b := range countryBlocks {
		if int(n) >= b.start && int(n) <= b.end && (best < 0 || b.end-b.start < countryBlocks[best].end-countryBlocks[best].start) {
			best = i
		}
	}
	if best < 0 {
		return "", "", false
	}
	return countryBlocks[best].name, countryBlocks[best].code, true
}

// CountryFor is CountryForHex, refined by the registration where there is one: Hong Kong and Macao register their own aircraft (B-H, B-K
// and B-L; B-M) but their addresses lie in China's block (Cathay's B-LRM is 780AB8), so the hex alone says China.
func CountryFor(hexAddr, tail string) (name, code string, ok bool) {
	name, code, ok = CountryForHex(hexAddr)
	if ok && code == "CN" && len(tail) > 2 && strings.HasPrefix(tail, "B-") {
		switch tail[2] {
		case 'H', 'K', 'L':
			return "Hong Kong", "HK", true
		case 'M':
			return "Macao", "MO", true
		}
	}
	return name, code, ok
}
