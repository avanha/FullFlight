package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The airline position report sent as label 83 (seen from United 787s and ZIPAIR), about every 30 minutes:
//
//	M78AZG0001PHNL,RJAA,302314, 28.71, 173.77,40000,258,- 84.1, 62503
//	KDEN,LIRF,280859, 49.49,   4.02,39002,258, 122.6, 29000
//
// An optional 4-character message number and 6-character flight, then origin, destination, DDHHMM, latitude
// and longitude in signed decimal degrees (the sign may be followed by spaces), altitude in feet, the
// direction the wind blows from, and the wind along the track (positive a tailwind: eastbound flights
// have positive values, westbound negative). The last number is unexplained. Read from samples, not a
// specification.
var aocRe = regexp.MustCompile(`^(?:([A-Z0-9]{4})([A-Z0-9]{2}\d{4}))?([A-Z]{4}),([A-Z]{4}),(\d{2})(\d{2})(\d{2}),\s*(-?)\s*(\d*\.\d+),\s*(-?)\s*(\d*\.\d+),\s*(-?\d+),\s*(\d+),\s*(-?)\s*(\d+\.\d+),\s*(\d+)`)

// The position report on label 16 from a Turkish-registered A320 (TC-NCE), every five minutes, with the full date and time:
//
//	PC25AG/09NOV26 16:17:00,34974,1648,  90,N 41.667 E 27.006
//
// An identifier (two letters and four), the date and time (UTC), the altitude in feet, two numbers whose meaning is not given (the
// second falls as the flight goes on: 197, 186, 179 ... 145 over an hour), and the position in decimal degrees (a longitude under one
// degree is written without its zero, ".794"). Read from 121 messages of one aircraft. The track and the altitudes are sound (a climb
// from Cologne, then FL370 across Bavaria, Austria and Hungary), but no ADS-B was kept for it to check the positions against.
var pcPosRe = regexp.MustCompile(`^([A-Z0-9]{6})/(\d{2})([A-Z]{3})(\d{2}) (\d\d):(\d\d):(\d\d),\s*(-?\d+),\s*(\d+),\s*(\d+),([NS])\s*(\d*\.\d+)\s+([EW])\s*(\d*\.\d+)\s*$`)

// parsePCPos decodes that report.
func parsePCPos(body string, r *Result) {
	m := pcPosRe.FindStringSubmatch(strings.TrimSpace(body))
	if m == nil {
		return
	}
	t, err := time.Parse("02Jan06 15:04:05", m[2]+m[3]+m[4]+" "+m[5]+":"+m[6]+":"+m[7])
	if err != nil {
		return
	}
	lat, _ := strconv.ParseFloat(m[12], 64)
	lon, _ := strconv.ParseFloat(m[14], 64)
	if lat > 90 || lon > 180 {
		return
	}
	if m[11] == "S" {
		lat = -lat
	}
	if m[13] == "W" {
		lon = -lon
	}
	alt, _ := strconv.ParseFloat(m[8], 64)
	r.Pos = posAt(lat, lon, &alt, t.UTC())
	r.Fields["id"] = m[1]
	r.Fields["at"] = t.UTC().Format("2006-01-02T15:04:05Z07:00")
	r.Fields["values"] = []string{m[9], m[10]} // unexplained
}

// The position report on label 15 from British Airways' Airbus, every five or ten minutes, in two layouts. The A380's (G-XLEI):
//
//	FST01EGLLKMIAN514642W00050960191380  15 P11C029076265269159  19011015
//
// and a shorter one (a short-haul Airbus, EGLL to Hamburg):
//
//	FST01EGLLEDDHN53721E0087707112  28  24     21261      379  20242011
//
// Both give the origin and destination, the position in decimal degrees (N51.4642 W000.5096; the short one has three decimals of
// latitude), the flight level (019), two numbers of four columns each that sum to a constant over the flight (1380 and 15 at the start,
// 1223 and 171 fifty minutes on: the fuel left, then the fuel used, by the way they run), the ground speed in knots, then the estimated
// arrival (HHMM) and the time of the report (HHMM). The A380's adds the temperature (P11, M46) and C and five groups of three digits
// of which the ground speed is the last. The short one has a group of five digits, which looks like a wind (212 degrees, 61 knots).
// The position, level and ground speed were checked against ADS-B for one flight of each; the groups left unnamed and the units of the
// fuel are not understood.
var (
	fstRe      = regexp.MustCompile(`^FST\d\d([A-Z]{4})([A-Z]{4})([NS])(\d{2})(\d{3,4})([EW])(\d{3})(\d{4})([ \d]{3})([ \d]{4})([ \d]{4})(.*?)\s+(\d{2})(\d{2})(\d{2})(\d{2})\s*$`)
	fstA380Re  = regexp.MustCompile(`^\s*([PM]\d+)C(\d{3})(\d{3})(\d{3})(\d{3})(\d{3})$`)
	fstShortRe = regexp.MustCompile(`^\s*(\d{5})\s+(\d{3})$`)
)

func parseFST(body string, r *Result) {
	m := fstRe.FindStringSubmatch(strings.TrimSpace(body))
	if m == nil {
		return
	}
	atof := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	atoi := func(s string) int { v, _ := strconv.Atoi(strings.TrimSpace(s)); return v }
	lat, lon := atof(m[4]+"."+m[5]), atof(m[7]+"."+m[8])
	hh, mm := atoi(m[15]), atoi(m[16])
	if lat > 90 || lon > 180 || hh > 23 || mm > 59 || atoi(m[13]) > 23 || atoi(m[14]) > 59 {
		return
	}
	var gs, text string
	fields := map[string]any{}
	if t := fstA380Re.FindStringSubmatch(m[12]); t != nil {
		temp := atoi(t[1][1:])
		if t[1][0] == 'M' {
			temp = -temp
		}
		gs = t[6]
		fields["temp_c"] = temp
		fields["values"] = []string{t[2], t[3], t[4], t[5]} // unexplained
		text = fmt.Sprintf(", %d°C", temp)
	} else if t := fstShortRe.FindStringSubmatch(m[12]); t != nil {
		gs = t[2]
		fields["values"] = []string{t[1]} // unexplained
	} else {
		return
	}
	if m[3] == "S" {
		lat = -lat
	}
	if m[6] == "W" {
		lon = -lon
	}
	alt := atof(strings.TrimSpace(m[9])) * 100
	r.Pos = &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: float64(hh*3600 + mm*60), Basis: BasisDay}
	for k, v := range fields {
		r.Fields[k] = v
	}
	r.Fields["origin"], r.Fields["dest"] = m[1], m[2]
	r.Fields["fuel_left"], r.Fields["fuel_used"] = atoi(m[10]), atoi(m[11]) // units not known
	r.Fields["gs_kt"] = atoi(gs)
	r.Fields["eta"] = m[13] + ":" + m[14] + "Z"
	g := atof(gs)
	r.GS = &g
	r.Text = fmt.Sprintf("Position %s at FL%s%s, ground speed %s kt; estimated arrival %s:%sZ at %s\nFuel left %d, used %d (units not known)",
		latlon(lat, lon), strings.TrimSpace(m[9]), text, gs, m[13], m[14], m[2], atoi(m[10]), atoi(m[11]))
}

// parseAOC decodes a label-83 airline position report, or the label-16 one above.
func parseAOC(body string, r *Result) {
	m := aocRe.FindStringSubmatch(body)
	if m == nil {
		if strings.HasPrefix(body, "FST") {
			parseFST(body, r)
			return
		}
		parsePCPos(body, r)
		return
	}
	atof := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	if m[1] != "" {
		r.Identity = parsePrefix(m[1] + m[2])
	}
	r.Fields["origin"], r.Fields["dest"] = m[3], m[4]
	day, hh, mm := atoi(m[5]), atoi(m[6]), atoi(m[7])
	if day < 1 || day > 31 || hh > 23 || mm > 59 {
		return
	}
	lat, lon := atof(m[9]), atof(m[11])
	if m[8] == "-" {
		lat = -lat
	}
	if m[10] == "-" {
		lon = -lon
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return
	}
	alt := atof(m[12])
	r.Pos = &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: float64(((day-1)*24+hh)*3600 + mm*60), Basis: BasisMonth}
	if dir := atof(m[13]); dir <= 360 {
		r.Wx = &Weather{WindDirDeg: &dir}
		r.Fields["wind_dir"] = dir
	}
	tail := atof(m[15])
	if m[14] == "-" {
		tail = -tail
	}
	r.Fields["tailwind_kt"] = tail
	r.Fields["extra"] = atoi(m[16]) // unexplained
}

func aocSummary(r Result) string {
	s := "Airline position report"
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
	return s
}
