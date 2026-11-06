package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Aircraft weather observations (AMDAR style) on label H1: a climb (A3), a descent (D3) or a stretch of the cruise (E3), a few
// points a minute or less apart, each with the temperature and the wind. Read from N956AK's traffic and checked against its track.
//
//	A3M2312343KSEAKLAX0378P005094005G0009             climb, sequence 2, dated the 31st, sent 23:43; the first line has no position
//	N47335W1221840455P003097006G0009                  latitude and longitude in degrees and tenths of minutes, altitude in tens of feet,
//	N47349W1221840541P001128010B0009                  temperature in degrees, wind direction and speed in knots, then a quality code
//	D3M630PHNLKLAXN33025W11936005443050M042232068G0009   descent, sequence 6, dated the 30th; the first line carries its time (05:44)
//	E301KSEAKLAXN46451W12208423523024M048201050G0009     cruise, dated the 1st
//
// The first line of a descent or a cruise report, and of a climb that starts on a position, says when it was taken, as hhmm (or
// the day and hhmm); a climb's lines have none, and the time in its header is that of the report, within half a minute of its last
// point. The newest point with a time is the message's position. A message can be days late, so the date is part of the position.
var (
	amdarHeadRe = regexp.MustCompile(`^(?:([AD])3([A-Z])(\d)(\d{2})(\d{4})?|E3(\d{2}))([A-Z]{4})([A-Z]{4})`)
	amdarLineRe = regexp.MustCompile(`^(?:N(\d{2})(\d{3})([WE])(\d{3})(\d{3})(\d{4}|\d{6})?)?(-\d{3}|\d{4})([PM]\d{3})(?:(\d{3})(\d{3})[GB])?\d{4}/?$`)
)

type amdarPoint struct {
	hasPos           bool
	lat, lon         float64
	alt, temp        float64
	dir, kt          float64
	hasWind, hasTime bool
	day, secs        int
}

func parseAMDAR(text string) (opsParsed, bool) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r", ""), "\n")
	h := amdarHeadRe.FindStringSubmatch(lines[0])
	if h == nil {
		return opsParsed{}, false
	}
	phase, day, hdrTime := map[string]string{"A": "climb", "D": "descent", "": "cruise"}[h[1]], atoi(h[4]), h[5]
	if h[6] != "" {
		day = atoi(h[6])
	}
	first := lines[0][len(h[0]):]
	var pts []amdarPoint
	for i, line := range append([]string{first}, lines[1:]...) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") && i > 0 || strings.HasPrefix(line, "XX") {
			continue // the closing checksum, and filler where the observations ran out
		}
		m := amdarLineRe.FindStringSubmatch(line)
		if m == nil {
			return opsParsed{}, false
		}
		p := amdarPoint{day: day, alt: atof(m[7]) * 10, temp: atof(m[8][1:])}
		if m[8][0] == 'M' {
			p.temp = -p.temp
		}
		if m[9] != "" {
			p.dir, p.kt, p.hasWind = atof(m[9]), atof(m[10]), true
		}
		if m[1] != "" {
			p.hasPos = true
			p.lat = float64(atoi(m[1])) + float64(atoi(m[2]))/10/60
			p.lon = float64(atoi(m[4])) + float64(atoi(m[5]))/10/60
			if m[3] == "W" {
				p.lon = -p.lon
			}
			t := m[6]
			if len(t) == 6 { // the day, then the time
				p.day, t = atoi(t[:2]), t[2:]
			}
			if t != "" && hhmm(t) != "" {
				p.hasTime, p.secs = true, atoi(t[:2])*3600+atoi(t[2:])*60
			}
		}
		pts = append(pts, p)
	}
	if len(pts) == 0 {
		return opsParsed{}, false
	}
	// The newest point that has a position and a time; a climb's points have none of their own, so the last takes the report's.
	best := -1
	for i, p := range pts {
		if p.hasPos && p.hasTime {
			best = i
			break
		}
	}
	if best < 0 && hdrTime != "" && hhmm(hdrTime) != "" {
		for i := len(pts) - 1; i >= 0; i-- {
			if pts[i].hasPos {
				best, pts[i].hasTime, pts[i].secs = i, true, atoi(hdrTime[:2])*3600+atoi(hdrTime[2:])*60
				break
			}
		}
	}
	o := OpsReport{Event: "AMDAR", Origin: h[7], Dest: h[8], Phase: phase, Info: map[string]string{"points": strconv.Itoa(len(pts))}}
	s := fmt.Sprintf("Weather observation, %s, %s", phase, route(o.Origin, o.Dest))
	r := opsParsed{rep: o}
	at := pts[len(pts)-1]
	if best >= 0 {
		at = pts[best]
		alt := at.alt
		r.pos = &Pos{Lat: at.lat, Lon: at.lon, AltFt: &alt, Secs: float64(at.day*100000 + at.secs), Basis: BasisUTCDay}
		s += fmt.Sprintf(": %s, %s", latlon(at.lat, at.lon), flightLevel(at.alt))
	}
	// When it was taken, for a report with no position to carry the time (a climb whose points have none): the store counts the report
	// from this, not from when it arrived, which may be days later.
	if at.hasTime {
		r.rep.Info["day"], r.rep.Info["time"] = fmt.Sprintf("%02d", at.day), fmt.Sprintf("%02d:%02dZ", at.secs/3600, at.secs%3600/60)
	} else if hhmm(hdrTime) != "" {
		r.rep.Info["day"], r.rep.Info["time"] = fmt.Sprintf("%02d", day), hdrTime[:2]+":"+hdrTime[2:]+"Z"
	}
	r.wx = &Weather{TempC: &at.temp}
	if at.hasWind && at.dir <= 360 && at.kt <= 250 {
		r.wx.WindDirDeg, r.wx.WindKt = &at.dir, &at.kt
	}
	r.summary = s
	for _, p := range pts {
		line := fmt.Sprintf("%s ft, %.0f°C", groupThousands(int(p.alt)), p.temp)
		if p.hasWind {
			line += fmt.Sprintf(", wind %03.0f°/%.0f kt", p.dir, p.kt)
		}
		if p.hasPos {
			line = latlon(p.lat, p.lon) + ", " + line
		}
		r.extra = append(r.extra, line)
	}
	return r, true
}
