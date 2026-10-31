package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// China Cargo's other position report (H1, also behind the satellite header #10B):
//
//	SB0122BA_B-221W  KLAXZSPD355
//	 62280-1533462154 35997-420196082010 7GX010014
//
// the registration, origin and destination, a number we cannot explain; then latitude and longitude in thousandths
// of a degree (signed), the UTC time HHMM, altitude in feet, temperature in tenths of a degree, the direction the
// wind blows from, its speed in knots, and twelve characters we cannot explain. The time is a time of day only.
// Read from samples, not a specification: messages sent on time match their own clocks to the minute.
var sbPosRe = regexp.MustCompile(`^SB0122BA_(\S+)\s+([A-Z]{4})([A-Z]{4})([ \d]{3})\s+(-?\d{5})\s?(-?\d{6})(\d{2})(\d{2})\s+(\d{5})([ -]\d{3})(\d{3})(\d{3})(.{12})\s*$`)

func parseSBPos(body string, r *Result) {
	m := sbPosRe.FindStringSubmatch(strings.TrimSpace(body))
	if m == nil {
		return
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(strings.TrimSpace(s)); return v }
	lat, lon := float64(atoi(m[5]))/1000, float64(atoi(m[6]))/1000
	hh, mm := atoi(m[7]), atoi(m[8])
	if lat > 90 || lat < -90 || lon > 180 || lon < -180 || hh > 23 || mm > 59 {
		return
	}
	alt := float64(atoi(m[9]))
	r.Pos = &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: float64(hh*3600 + mm*60), Basis: BasisDay}
	temp := float64(atoi(m[10])) / 10
	dir, kt := float64(atoi(m[11])), float64(atoi(m[12]))
	w := &Weather{TempC: &temp}
	if dir <= 360 && kt <= 250 {
		w.WindDirDeg, w.WindKt = &dir, &kt
	}
	r.Wx = w
	r.Fields["reg"] = m[1]
	r.Fields["origin"], r.Fields["dest"] = m[2], m[3]
	r.Fields["extra"] = m[4] + m[13] // unexplained
}

func sbPosSummary(r Result) string {
	if r.Pos == nil {
		return "Position report"
	}
	s := "Position report " + latlon(r.Pos.Lat, r.Pos.Lon)
	if r.Pos.AltFt != nil {
		s += fmt.Sprintf(" %.0f ft", *r.Pos.AltFt)
	}
	if o, _ := r.Fields["origin"].(string); o != "" {
		d, _ := r.Fields["dest"].(string)
		s += ", " + o + "→" + d
	}
	return s
}
