package decoder

import (
	"fmt"
	"regexp"
	"strconv"
)

// The airline position report that starts "*POS" (label H1; seen from UPS 747s), about every ten minutes:
//
//	*POS
//	10280938N3426W11820222249025727M2071494
//
// MMDDHHMM in UTC (it has a date, so the time is known even if the message is delivered days late), latitude
// DDMM, longitude DDDMM, flight level, the direction the wind blows from, wind speed in knots, Mach in
// thousandths, temperature (M minus, P plus, whole degrees), one digit we cannot explain, and a four digit
// number that falls steadily through a flight (fuel on board, in hundreds of pounds, to judge by the rate).
// Read from samples, not a specification: messages sent on time match their own clocks to the minute.
var posRptRe = regexp.MustCompile(`^\*POS\r?\n(\d{2})(\d{2})(\d{2})(\d{2})([NS])(\d{2})(\d{2})([EW])(\d{3})(\d{2})(\d{3})(\d{3})(\d{3})(\d{3})([MP])(\d{2})(\d)(\d{4})\s*$`)

func parsePosRpt(body string, r *Result) {
	m := posRptRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	month, day, hh, mm := atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4])
	lat := float64(atoi(m[6])) + float64(atoi(m[7]))/60
	lon := float64(atoi(m[9])) + float64(atoi(m[10]))/60
	if month < 1 || month > 12 || day < 1 || day > 31 || hh > 23 || mm > 59 || lat > 90 || lon > 180 || atoi(m[7]) > 59 || atoi(m[10]) > 59 {
		return
	}
	if m[5] == "S" {
		lat = -lat
	}
	if m[8] == "W" {
		lon = -lon
	}
	alt := float64(atoi(m[11])) * 100
	r.Pos = &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: float64(month*1000000 + day*10000 + hh*100 + mm), Basis: BasisDate}
	dir, kt, temp := float64(atoi(m[12])), float64(atoi(m[13])), float64(atoi(m[16]))
	if m[15] == "M" {
		temp = -temp
	}
	w := &Weather{TempC: &temp}
	if dir <= 360 && kt <= 250 {
		w.WindDirDeg, w.WindKt = &dir, &kt
	}
	r.Wx = w
	r.Fields["mach"] = float64(atoi(m[14])) / 1000
	r.Fields["extra"] = atoi(m[17]) // unexplained
	r.Fields["value"] = atoi(m[18]) // falls through the flight: fuel on board, probably
}

func posRptSummary(r Result) string {
	if r.Pos == nil {
		return "Position report"
	}
	s := "Position report " + latlon(r.Pos.Lat, r.Pos.Lon)
	if r.Pos.AltFt != nil {
		s += fmt.Sprintf(" FL%03.0f", *r.Pos.AltFt/100)
	}
	if mach, ok := r.Fields["mach"].(float64); ok && mach > 0.4 {
		s += fmt.Sprintf(", M%.3f", mach)
	}
	return s
}
