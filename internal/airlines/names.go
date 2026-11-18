package airlines

import "strings"

// names are the airlines by their ICAO designator, as their callsigns start: every designator in icaoByIATA, and the others seen on
// the aircraft we follow. The name is the airline's usual one, not its legal name.
var names = map[string]string{
	"UAL": "United", "AAL": "American", "DAL": "Delta", "SWA": "Southwest", "JBU": "JetBlue", "ASA": "Alaska", "HAL": "Hawaiian",
	"NKS": "Spirit", "FFT": "Frontier", "AAY": "Allegiant", "ACA": "Air Canada", "WJA": "WestJet", "AMX": "Aeroméxico", "LAN": "LATAM Chile",
	"TAM": "LATAM Brasil", "AVA": "Avianca", "CMP": "Copa", "BAW": "British Airways", "VIR": "Virgin Atlantic", "EIN": "Aer Lingus",
	"DLH": "Lufthansa", "SWR": "Swiss", "AUA": "Austrian", "BEL": "Brussels Airlines", "KLM": "KLM", "AFR": "Air France", "IBE": "Iberia",
	"TAP": "TAP Air Portugal", "ITY": "ITA Airways", "SAS": "SAS", "FIN": "Finnair", "LOT": "LOT Polish", "THY": "Turkish Airlines",
	"RYR": "Ryanair", "EZY": "easyJet", "WZZ": "Wizz Air", "UAE": "Emirates", "QTR": "Qatar Airways", "ETD": "Etihad", "SVA": "Saudia",
	"ETH": "Ethiopian", "SAA": "South African", "MSR": "EgyptAir", "AIC": "Air India", "IGO": "IndiGo", "SIA": "Singapore Airlines",
	"THA": "Thai Airways", "MAS": "Malaysia Airlines", "GIA": "Garuda Indonesia", "PAL": "Philippine Airlines", "HVN": "Vietnam Airlines",
	"CPA": "Cathay Pacific", "HDA": "Cathay Dragon", "EVA": "EVA Air", "CAL": "China Airlines", "JAL": "Japan Airlines", "ANA": "All Nippon",
	"KAL": "Korean Air", "AAR": "Asiana", "CCA": "Air China", "CES": "China Eastern", "CSN": "China Southern", "CHH": "Hainan Airlines",
	"QFA": "Qantas", "ANZ": "Air New Zealand", "VOZ": "Virgin Australia", "JST": "Jetstar", "FJI": "Fiji Airways",
	"CKK": "China Cargo", "TZP": "Zipair", "FDX": "FedEx", "UPS": "UPS", "NCA": "Nippon Cargo", "CLX": "Cargolux", "AHK": "Air Hong Kong",
	"GTI": "Atlas Air", "PAC": "Polar Air Cargo", "CKS": "Kalitta Air", "BCS": "European Air Transport",
}

// Name is the airline that flies under an ICAO designator ("BAW" is British Airways).
func Name(icao string) (string, bool) {
	n, ok := names[strings.ToUpper(icao)]
	return n, ok
}

// OfCallsign is the ICAO designator at the start of an airline callsign: three letters and then the flight number ("BAW3G", "UAL26"). An
// aircraft's own registration (N575UP, G-XLEI) and the callsigns of private flights do not start that way.
func OfCallsign(callsign string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(callsign))
	if len(c) < 4 || c[3] < '0' || c[3] > '9' {
		return "", false
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return "", false
		}
	}
	return c[:3], true
}
