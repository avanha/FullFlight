package decoder

import (
	"fmt"
	"regexp"
	"strings"
)

// The Airbus A320 family's ACMS reports, on label H1, as American and United send them (Airbus's A350 and A380 send the same design; see a350.go):
//
//	A321,050415,1,1,TB000000/REP001,00,00,4/CCN900UW,NOV01,125824,KCLT,KBOS,0436/C0TIA05AAL031001/C106,85402,...
//	A321,050882,1,1,TB000000/REP035,00,00,4//C1035,APU RUN/C2N900UW,261105171819,171819,KPHX,KCMH,AAL1152,4200/C3...
//	A321,051035,1,1,TB000000/REP291,00,00,4/\r\nTRP KORD KSEA  8 8\r\n/A1 054101, 42.9940,- 94.9386,360,156.0,454,0.000235,...
//
// Most start with the tail, the day and time, the route and the flight number, and then numbers whose meaning is not given; the ones
// with a title (REP035 "APU RUN") say what raised them. The TRP reports (REP191, REP291) are a trajectory: every three minutes a
// time, a position and a heading, then more numbers. Read from the traffic of two aircraft; only those values are read.
var (
	acmsHeadRe  = regexp.MustCompile(`^(A3\d\d),\d+,\d+,\d+,TB\w+/REP(\d+),[^/]*/`)
	acmsRteRe   = regexp.MustCompile(`/C[C0-9]([A-Z0-9-]{4,7}),([A-Z]{3})(\d\d),(\d{6}), ?([A-Z]{3,4}),([A-Z]{4})(?:,(\d{3,4}))?`)
	acmsFullRe  = regexp.MustCompile(`/C2([A-Z0-9-]{4,7}),(\d{6})(\d{6}),\d{6},([A-Z]{4}),([A-Z]{4}),([A-Z]{3}\d{1,4})`)
	acmsTitleRe = regexp.MustCompile(`/C1(\d{3,4}),([^/,]+)/`)
	acmsAirline = regexp.MustCompile(`(AAL|UAL|DAL|ASA|ACA|SWA|JBU)\d`)
	trpHeadRe   = regexp.MustCompile(`(?m)^TRP ([A-Z]{4}) ([A-Z]{4}) +(\d+) +(\d+)`)
	trpPointRe  = regexp.MustCompile(`(?:^|[/\s])(\d{6}), *(-? *\d{1,2}\.\d{4}), *(-? *\d{1,3}\.\d{4}), *(\d{1,3}),`)
)

func signed(s string) float64 { return atof(strings.ReplaceAll(s, " ", "")) }

func parseACMS(text string) (opsParsed, bool) {
	t := strings.TrimSpace(text)
	h := acmsHeadRe.FindStringSubmatch(strings.ReplaceAll(t, "\r", ""))
	if h == nil {
		return opsParsed{}, false
	}
	t = strings.ReplaceAll(t, "\r", "")
	o := OpsReport{Event: "REP" + h[2], Info: map[string]string{"type": h[1]}}
	var lines []string
	var p *Pos
	if th := trpHeadRe.FindStringSubmatch(t); th != nil {
		o.Origin, o.Dest, o.Event = th[1], th[2], "trajectory"
		type pt struct {
			hms      string
			lat, lon float64
			hdg      int
		}
		var pts []pt
		for _, m := range trpPointRe.FindAllStringSubmatch(t, -1) {
			lat, lon := signed(m[2]), signed(m[3])
			hh, mm, ss := atoi(m[1][:2]), atoi(m[1][2:4]), atoi(m[1][4:])
			if hh > 23 || mm > 59 || ss > 59 || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
				continue
			}
			pts = append(pts, pt{m[1], lat, lon, atoi(m[4])})
		}
		if len(pts) == 0 {
			return opsParsed{}, false
		}
		for _, q := range pts {
			lines = append(lines, fmt.Sprintf("%s:%s:%sZ %s, heading %d°", q.hms[:2], q.hms[2:4], q.hms[4:], latlon(q.lat, q.lon), q.hdg))
		}
		last := pts[len(pts)-1]
		secs := float64(atoi(last.hms[:2])*3600 + atoi(last.hms[2:4])*60 + atoi(last.hms[4:]))
		p = &Pos{Lat: last.lat, Lon: last.lon, Secs: secs, Basis: BasisDay}
		o.Info["points"] = fmt.Sprint(len(pts))
		s := fmt.Sprintf("%s trajectory report %s at %s: %s", h[1], route(o.Origin, o.Dest), hmZ(last.hms[:4]), latlon(last.lat, last.lon))
		if len(pts) > 1 {
			s += fmt.Sprintf(" (newest of %d points)", len(pts))
		}
		return opsParsed{rep: o, pos: p, summary: s, extra: lines}, true
	}
	var tail, flightWord string
	if m := acmsFullRe.FindStringSubmatch(t); m != nil {
		tail, flightWord = m[1], m[6]
		o.Origin, o.Dest = m[4], m[5]
		if at, ok := eventTime(m[2], m[3], true); ok {
			o.At = at.Format("2006-01-02T15:04:05Z07:00")
		}
		o.Callsign = m[6]
	} else if m := acmsRteRe.FindStringSubmatch(t); m != nil {
		tail = m[1]
		o.Info["day"], o.Info["month"], o.Info["time"] = m[3], m[2], hmZ(m[4][:4])
		o.Origin, o.Dest = icaoOf(strings.TrimSpace(m[5])), m[6]
		if a := acmsAirline.FindStringSubmatch(t); a != nil && m[7] != "" {
			o.Callsign, o.Flight = opsFlight(a[1], m[7])
			o.Airline = opsAirline(a[1])
			flightWord = o.Callsign
		}
	} else {
		return opsParsed{}, false
	}
	if o.Origin == "XXXX" {
		o.Origin = ""
	}
	if o.Dest == "XXXX" {
		o.Dest = ""
	}
	if o.Callsign != "" && o.Flight == "" {
		o.Callsign, o.Flight = opsFlight(o.Callsign[:3], o.Callsign[3:])
		o.Airline = opsAirline(flightWord[:3])
	}
	o.Reg = tail
	what := fmt.Sprintf("%s report %s", h[1], h[2])
	if m := acmsTitleRe.FindStringSubmatch(t); m != nil {
		o.Info["title"] = strings.TrimSpace(m[2])
		what += ": " + o.Info["title"]
		lines = append(lines, "Raised by: "+o.Info["title"])
	}
	s := what
	if o.Callsign != "" {
		s += " " + o.Callsign
	}
	if r := route(o.Origin, o.Dest); r != "" {
		s += " " + r
	}
	if o.Info["time"] != "" { // these are held and delivered days late: say when it was written
		s += fmt.Sprintf(" at %s on %s %s", o.Info["time"], o.Info["day"], o.Info["month"])
	}
	return opsParsed{rep: o, summary: s, extra: lines, tail: tail}, true
}
