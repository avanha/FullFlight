// Package airlines maps IATA airline designators to ICAO ones.
package airlines

import (
	"regexp"
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

var icaoSet = func() map[string]bool {
	m := make(map[string]bool, len(icaoByIATA))
	for _, v := range icaoByIATA {
		m[v] = true
	}
	return m
}()

// ICAO returns the ICAO designator for an IATA one ("UA" -> "UAL").
func ICAO(iata string) (string, bool) {
	v, ok := icaoByIATA[strings.ToUpper(iata)]
	return v, ok
}

// IsICAO reports whether code is a known ICAO airline designator.
func IsICAO(code string) bool { return icaoSet[strings.ToUpper(code)] }

// IsIATA reports whether code is a known IATA airline designator.
func IsIATA(code string) bool { _, ok := ICAO(code); return ok }

var iataByICAO = func() map[string]string {
	m := make(map[string]string, len(icaoByIATA))
	for k, v := range icaoByIATA {
		m[v] = k
	}
	return m
}()

var (
	icaoFlightRe = regexp.MustCompile(`^([A-Z]{3})0*(\d{1,4}[A-Z]?)$`)
	iataFlightRe = regexp.MustCompile(`^([A-Z0-9]{2})0*(\d{1,4}[A-Z]?)$`)
)

// Forms returns a flight identifier in both its ICAO callsign form ("CPA113") and its IATA
// flight-number form ("CX113"), given either. Leading zeros in the number are dropped. Both are
// empty if id is not a flight identifier or its airline is not in the table.
func Forms(id string) (icao, iata string) {
	id = strings.ToUpper(strings.TrimSpace(id))
	if m := icaoFlightRe.FindStringSubmatch(id); m != nil {
		if code := iataByICAO[m[1]]; code != "" {
			return m[1] + m[2], code + m[2]
		}
	}
	if m := iataFlightRe.FindStringSubmatch(id); m != nil {
		if des, ok := icaoByIATA[m[1]]; ok {
			return des + m[2], m[1] + m[2]
		}
	}
	return "", ""
}
