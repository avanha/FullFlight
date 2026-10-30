package poller

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/avanha/fullflight/internal/airlines"
)

var flightIdent = regexp.MustCompile(`^([A-Z0-9]{2}|[A-Z]{3})(\d{1,4})([A-Z]?)$`)

// CallsignCandidates returns the callsigns to try for a flight number such as
// "UA869", most likely first: the ICAO form, then zero-padded and bare numbers.
func CallsignCandidates(flight string) ([]string, error) {
	m := flightIdent.FindStringSubmatch(strings.ToUpper(flight))
	if m == nil {
		return nil, fmt.Errorf("%q is not a flight number", flight)
	}
	des, numStr, suffix := m[1], m[2], m[3]
	if len(des) == 2 {
		icao, ok := airlines.ICAO(des)
		if !ok {
			return nil, fmt.Errorf("unknown airline code %q; enter the ICAO callsign instead (e.g. UAL869)", des)
		}
		des = icao
	}
	n, _ := strconv.Atoi(numStr)
	out := []string{fmt.Sprintf("%s%d%s", des, n, suffix)}
	if padded := fmt.Sprintf("%s%04d%s", des, n, suffix); padded != out[0] {
		out = append(out, padded)
	}
	return out, nil
}
