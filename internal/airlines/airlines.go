// Package airlines maps IATA airline designators to ICAO ones.
package airlines

import "strings"

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
