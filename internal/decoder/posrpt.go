package decoder

import (
	"fmt"
	"regexp"
	"strconv"
)

// The position report sent as label 80 by Delta and Fiji Airways aircraft, about every 15 to 30 minutes:
//
//	3N01 POSRPT 0039/30 KLAX/ZSPD .N501DN
//	/POS N34414W119598/ALT 252/MCH 759/FOB 2022
//	/TME 1941/WND 239 032/OAT -029/TAS 0462/ETA 0925
//
// An optional message number and flight in front (M57ADL0170), then flight number and day of the month, origin
// and destination, and the tail. Position is degrees and tenths of minutes (N34414 is 34 41.4 N), ALT is a flight
// level, MCH is Mach in thousandths, FOB fuel on board (units unknown), TME the time in UTC (hhmm; the stations
// stamp arrival about three minutes early, so on-time messages arrive at TME minus 3), WND the direction the wind
// blows from (negative for 180 to 360: -142 is 218) and its speed in knots, OAT in whole degrees C, TAS in knots
// and ETA at the destination (hhmm, or dashes or blank). Read from samples, not a specification.
var posrptRe = regexp.MustCompile(`^(?:([A-Z0-9]{4})([A-Z0-9]{2}\d{4}))?\d?N01\s+POSRPT\s+(\d{4})/(\d{2})\s+([A-Z]{4})/([A-Z]{4})\s+\.([A-Z0-9-]+)\s*\r?\n\s*/POS ([NS])(\d{2})(\d{3})([EW])(\d{3})(\d{3})/ALT (-?\d+)/MCH (\d+)/FOB (\d+)\s*\r?\n\s*/TME (\d{2})(\d{2})/WND (-?\d+) (\d+)/OAT ([-+]?\d+)/TAS (\d+)/ETA\s*(\d{4}|-*)`)

// Capture groups: 1 message number, 2 flight (both optional), 3 flight number, 4 day of the month, 5 origin,
// 6 destination, 7 tail, 8 N/S, 9-10 latitude degrees and tenths of minutes, 11 E/W, 12-13 longitude, 14 flight
// level, 15 Mach, 16 fuel, 17-18 hour and minute, 19 wind direction, 20 wind speed, 21 OAT, 22 TAS, 23 ETA.
func parsePOSRPT(body string, r *Result) {
	m := posrptRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	if m[1] != "" {
		r.Identity = parsePrefix(m[1] + m[2])
	}
	r.Identity.Tail = m[7]
	day, hh, mm := atoi(m[4]), atoi(m[17]), atoi(m[18])
	r.Fields["flight_no"], r.Fields["day"] = m[3], day
	r.Fields["origin"], r.Fields["dest"] = m[5], m[6]
	r.Fields["fob"] = atoi(m[16])
	r.Fields["tas"] = atoi(m[22])
	if m[23] != "" && m[23][0] != '-' {
		r.Fields["eta"] = m[23]
	}
	lat := float64(atoi(m[9])) + float64(atoi(m[10]))/10/60
	lon := float64(atoi(m[12])) + float64(atoi(m[13]))/10/60
	if hh > 23 || mm > 59 || day < 1 || day > 31 || lat > 90 || lon > 180 {
		return
	}
	if m[8] == "S" {
		lat = -lat
	}
	if m[11] == "W" {
		lon = -lon
	}
	alt := float64(atoi(m[14])) * 100
	// The time of day with the flight's day of the month: see ResolveDayTime.
	r.Pos = &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: float64(day*100000 + hh*3600 + mm*60), Basis: BasisDayHint}
	dir, kt, temp := float64(atoi(m[19])), float64(atoi(m[20])), float64(atoi(m[21]))
	if dir < 0 {
		dir += 360
	}
	w := &Weather{TempC: &temp}
	if dir >= 0 && dir <= 360 && kt <= 250 {
		w.WindDirDeg, w.WindKt = &dir, &kt
	}
	r.Wx = w
	r.Fields["mach"] = float64(atoi(m[15])) / 1000
}

func posrptSummary(r Result) string {
	s := "Position report"
	if o, ok := r.Fields["origin"].(string); ok {
		s += fmt.Sprintf(" %s→%v", o, r.Fields["dest"])
	}
	if r.Pos == nil {
		return s
	}
	s += " " + latlon(r.Pos.Lat, r.Pos.Lon)
	if r.Pos.AltFt != nil {
		s += fmt.Sprintf(" FL%03.0f", *r.Pos.AltFt/100)
	}
	if mach, ok := r.Fields["mach"].(float64); ok && mach > 0.4 {
		s += fmt.Sprintf(", M%.3f", mach)
	}
	return s
}
