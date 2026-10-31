package decoder

import (
	"fmt"
	"regexp"
	"strconv"
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
var aocRe = regexp.MustCompile(`^(?:([A-Z0-9]{4})([A-Z0-9]{2}\d{4}))?([A-Z]{4}),([A-Z]{4}),(\d{2})(\d{2})(\d{2}),\s*(-?)\s*(\d+\.\d+),\s*(-?)\s*(\d+\.\d+),\s*(-?\d+),\s*(\d+),\s*(-?)\s*(\d+\.\d+),\s*(\d+)`)

// parseAOC decodes a label-83 airline position report.
func parseAOC(body string, r *Result) {
	m := aocRe.FindStringSubmatch(body)
	if m == nil {
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
