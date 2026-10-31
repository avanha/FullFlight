package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The Boeing 787 ACMF periodic position report, sent as label H1 by United's N249xx (and others)
// roughly every 18 minutes, each carrying a fix every 3 minutes (every minute in descent):
//
//	EB0116AB_9 ,333, N24993,UAL143    ,  87,301026,22:53:35,KDEN,RJAA,16591
//	BSM5870000021
//	N5810.2,W14513.5,302238, 35998,-49.5,215,079,ER,00000,0,0,
//
// A long report is split over several ACARS blocks and only the first carries the header, so a
// block of position lines alone is decoded too. Read from samples, not a specification: the
// header's second and fifth fields and the last three position fields are unexplained.
var (
	acmfHeader = regexp.MustCompile(`^[A-Z]{2}\d{4}[A-Z]{2}_\d\s*,\s*(\d+),\s*([A-Z0-9-]+)\s*,\s*([A-Z0-9]+)\s*,\s*(\d+),(\d{6}),(\d{2}:\d{2}:\d{2}),([A-Z]{4}),([A-Z]{4}),\s*(\d+)`)
	// lat, lon (degrees and tenths of minutes), DDHHMM, altitude ft, SAT C, wind from and knots, phase.
	acmfPoint = regexp.MustCompile(`(?m)^([NS])(\d{2})(\d{2}\.\d),([EW])(\d{3})(\d{2}\.\d),(\d{2})(\d{2})(\d{2}),\s*(-?\d+),\s*(-?\d+(?:\.\d)?),(\d{1,3}),(\d{1,3}),([A-Z]{0,2}),`)
)

var acmfPhases = map[string]string{"ER": "en route", "CL": "climb", "DC": "descent"}

// ACMFPoint is one fix of an ACMF report. Its time has minute resolution.
type ACMFPoint struct {
	Lat, Lon    float64
	SecsInMonth int64 // seconds since 00:00 UTC on the 1st; see ResolveMonthSeconds
	AltFt       float64
	TempC       float64
	WindDirDeg  float64
	WindKt      float64
	Phase       string // ER, CL or DC as sent
}

// parseACMF reads the header (if the block has one) and every position line.
func parseACMF(body string, r *Result) {
	body = strings.ReplaceAll(body, "\r", "")
	if m := acmfHeader.FindStringSubmatch(body); m != nil {
		r.Identity.Tail = m[2]
		r.Identity.Callsign = m[3]
		r.Fields["report_time"] = strings.ReplaceAll(m[6], ":", "")
		r.Fields["origin"], r.Fields["dest"] = m[7], m[8]
		r.Fields["elapsed_s"], _ = strconv.Atoi(m[9]) // counts seconds; its zero point is not known
		r.Fields["report_no"], _ = strconv.Atoi(m[1])
		r.Fields["leg_no"], _ = strconv.Atoi(m[4])
	}
	atof := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	var pts []ACMFPoint
	for _, m := range acmfPoint.FindAllStringSubmatch(body, -1) {
		day, hh, mm := atoi(m[7]), atoi(m[8]), atoi(m[9])
		if day < 1 || day > 31 || hh > 23 || mm > 59 {
			continue
		}
		p := ACMFPoint{
			Lat:         atof(m[2]) + atof(m[3])/60,
			Lon:         atof(m[5]) + atof(m[6])/60,
			SecsInMonth: int64(((day-1)*24+hh)*3600 + mm*60),
			AltFt:       atof(m[10]),
			TempC:       atof(m[11]),
			WindDirDeg:  atof(m[12]),
			WindKt:      atof(m[13]),
			Phase:       m[14],
		}
		if m[1] == "S" {
			p.Lat = -p.Lat
		}
		if m[4] == "W" {
			p.Lon = -p.Lon
		}
		pts = append(pts, p)
	}
	if len(pts) == 0 {
		return
	}
	r.Fields["points"] = pts
	// The message carries one position: the newest, which is the last line.
	last := pts[len(pts)-1]
	alt := last.AltFt
	r.Pos = &Pos{Lat: last.Lat, Lon: last.Lon, AltFt: &alt, Secs: float64(last.SecsInMonth), Basis: BasisMonth}
	if last.WindDirDeg <= 360 && last.WindKt <= 250 {
		r.Wx = &Weather{WindDirDeg: &last.WindDirDeg, WindKt: &last.WindKt, TempC: &last.TempC}
	} else {
		r.Wx = &Weather{TempC: &last.TempC}
	}
	// Text lists every fix, since the message carries only the newest on the map.
	lines := []string{acmfSummary(*r)}
	for _, p := range pts {
		t := fmt.Sprintf("%02d %02d:%02d", p.SecsInMonth/86400+1, p.SecsInMonth%86400/3600, p.SecsInMonth%3600/60)
		lines = append(lines, fmt.Sprintf("%sZ  %s  %.0f ft  %.1f°C  wind %03.0f°/%.0fkt  %s",
			t, latlon(p.Lat, p.Lon), p.AltFt, p.TempC, p.WindDirDeg, p.WindKt, phaseName(p.Phase)))
	}
	r.Text = strings.Join(lines, "\n")
}

func phaseName(code string) string {
	if n, ok := acmfPhases[code]; ok {
		return n
	}
	return code
}

// acmfSummary is the one-line timeline entry.
func acmfSummary(r Result) string {
	s := "ACMF report"
	if o, ok := r.Fields["origin"].(string); ok {
		s += fmt.Sprintf(" %s→%v", o, r.Fields["dest"])
	}
	pts, _ := r.Fields["points"].([]ACMFPoint)
	if r.Pos == nil || len(pts) == 0 {
		return s
	}
	s += " " + latlon(r.Pos.Lat, r.Pos.Lon)
	if r.Pos.AltFt != nil {
		s += fmt.Sprintf(" FL%03.0f", *r.Pos.AltFt/100)
	}
	if ph := phaseName(pts[len(pts)-1].Phase); ph != "" {
		s += " " + ph
	}
	if len(pts) > 1 {
		s += fmt.Sprintf(", %d fixes", len(pts))
	}
	return s
}
