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

// parseAOC decodes a label-83 airline position report, or the label-16 one above.
func parseAOC(body string, r *Result) {
	m := aocRe.FindStringSubmatch(body)
	if m == nil {
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
