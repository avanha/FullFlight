package poller

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// icaoByIATA maps common IATA airline designators to ICAO ones. A flight the
// user types as "UA869" is broadcast as the callsign "UAL869". Anything not
// listed can still be watched by typing the ICAO form ("UAL869").
var icaoByIATA = map[string]string{
	"UA": "UAL", "AA": "AAL", "DL": "DAL", "WN": "SWA", "B6": "JBU", "AS": "ASA", "HA": "HAL",
	"NK": "NKS", "F9": "FFT", "G4": "AAY", "AC": "ACA", "WS": "WJA", "AM": "AMX", "LA": "LAN",
	"AV": "AVA", "CM": "CMP", "BA": "BAW", "VS": "VIR", "EI": "EIN", "LH": "DLH", "LX": "SWR",
	"OS": "AUA", "SN": "BEL", "KL": "KLM", "AF": "AFR", "IB": "IBE", "TP": "TAP", "AZ": "ITY",
	"SK": "SAS", "AY": "FIN", "LO": "LOT", "TK": "THY", "FR": "RYR", "U2": "EZY", "W6": "WZZ",
	"EK": "UAE", "QR": "QTR", "EY": "ETD", "SV": "SVA", "ET": "ETH", "SA": "SAA", "MS": "MSR",
	"AI": "AIC", "6E": "IGO", "SQ": "SIA", "TG": "THA", "MH": "MAS", "GA": "GIA", "PR": "PAL",
	"VN": "HVN", "CX": "CPA", "KA": "HDA", "BR": "EVA", "CI": "CAL", "JL": "JAL", "NH": "ANA",
	"KE": "KAL", "OZ": "AAR", "CA": "CCA", "MU": "CES", "CZ": "CSN", "HU": "CHH", "QF": "QFA",
	"NZ": "ANZ", "VA": "VOZ", "JQ": "JST",
}

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
		icao, ok := icaoByIATA[des]
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
