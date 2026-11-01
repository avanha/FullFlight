package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Engine and performance reports from Boeing 787s (United, Air Canada), label H1. They are maintenance data, but each
// starts with the flight, the route and the full date and time of the event, and most carry the aircraft's position, so
// they say where an aircraft was and when even when little else is heard from it (and when they are delivered days late).
//
//	/ILNGE7X.CLB006UA N249932UAL177    261024000658KDENLIRF-0750...   an engine event: CLB climb, TOF take-off, SCR cruise
//	APM    1 N24993         UAL182  KDENEDDF211026232013,             an aircraft performance report
//	<701>IEG N24993UAL182    ER  72211026232013KDENEDDF...            an engine report
//
// The engine events give the date as yymmdd and the others as ddmmyy; both have hhmmss after it. Read from samples, not
// a specification: what the other numbers are is not known.
var (
	engineEvtRe = regexp.MustCompile(`^/ILNGE7X\.([A-Z0-9]{3})(\d{3})[A-Z]{2} ([A-Z0-9-]{5,6}?)(\d)([A-Z]{3}\d{1,4}) +(\d{6})(\d{6})([A-Z]{4})([A-Z]{4})`)
	enginePosRe = regexp.MustCompile(`([+-]\d{8})([+-]\d{8})([+-]\d{8})`)
	apmRe       = regexp.MustCompile(`^APM +(\d+) +([A-Z0-9-]{5,6}) +([A-Z]{3}\d{1,4}) +([A-Z]{4})([A-Z]{4})(\d{6})(\d{6}),`)
	apmPosRe    = regexp.MustCompile(`[ ,](-?\d{1,2}\.\d{4}),,,\s*\n\s*(-?\d{1,3}\.\d{4}),,,`)
	apmAltRe    = regexp.MustCompile(`\.\d{3},,,\s*[\d.]+,,,\s*-?[\d.]+,,,\s*(\d{5}),,,`)
	engReportRe = regexp.MustCompile(`^<(\d{3})>[A-Z]{3} ([A-Z0-9-]{5,6}?)([A-Z]{3}\d{1,4}) ER \d{2}(\d{6})(\d{6})([A-Z]{4})([A-Z]{4})`)
	engRepPosRe = regexp.MustCompile(`(\d{5})\d{7}(-?\d{3}) (-?\d{6}) (-?\d{6,7}) `)
)

var engineEvents = map[string]struct{ name, phase string }{
	"TOF": {"take-off", "takeoff"}, "CLB": {"climb", "climb"}, "SCR": {"cruise", "cruise"},
}

// eventTime reads a date and a time of day (hhmmss) as a UTC time. The date is yymmdd or ddmmyy.
func eventTime(date, hms string, yymmdd bool) (time.Time, bool) {
	layout := "020106 150405"
	if yymmdd {
		layout = "060102 150405"
	}
	t, err := time.Parse(layout, date+" "+hms)
	return t.UTC(), err == nil
}

func (o *OpsReport) setFlight(code, tail string) {
	o.Callsign, o.Flight = opsFlight(code[:3], code[3:])
	o.Reg = tail
	o.Airline = opsAirline(code[:3])
}

// posAt makes the position of a report with a full date; seconds are not kept.
func posAt(lat, lon float64, altFt *float64, t time.Time) *Pos {
	return &Pos{Lat: lat, Lon: lon, AltFt: altFt, Secs: float64(int(t.Month())*1000000 + t.Day()*10000 + t.Hour()*100 + t.Minute()), Basis: BasisDate}
}

func parseEngine(text string) (opsParsed, bool) {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "/ILNGE7X.<"): // a later block of an engine report, run on to the next one
		return parseEngineReport(spaces.ReplaceAllString(strings.TrimPrefix(t, "/ILNGE7X."), " "))
	case strings.HasPrefix(t, "/ILNGE7X."):
		return parseEngineEvent(spaces.ReplaceAllString(t, " "))
	case strings.HasPrefix(t, "APM"):
		return parseAPM(t)
	case strings.HasPrefix(t, "<"):
		return parseEngineReport(spaces.ReplaceAllString(t, " "))
	}
	return opsParsed{}, false
}

func parseEngineEvent(t string) (opsParsed, bool) {
	m := engineEvtRe.FindStringSubmatch(t)
	if m == nil {
		return opsParsed{}, false
	}
	at, ok := eventTime(m[6], m[7], true)
	if !ok {
		return opsParsed{}, false
	}
	ev := engineEvents[m[1]]
	o := OpsReport{Event: m[1], Origin: m[8], Dest: m[9], Phase: ev.phase, At: at.Format(time.RFC3339), Info: map[string]string{"engine": m[4]}}
	if o.Origin == o.Dest { // an arrival's reports name the airport twice
		o.Origin = ""
	}
	o.setFlight(m[5], m[3])
	var p *Pos
	if ev.phase == "takeoff" {
		o.OffAt = o.At
	}
	if g := enginePosRe.FindStringSubmatch(t); g != nil {
		lat, lon := float64(atoi(g[2][1:]))/1e5, float64(atoi(g[3][1:]))/1e5
		if g[2][0] == '-' {
			lat = -lat
		}
		if g[3][0] == '-' {
			lon = -lon
		}
		if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && (lat != 0 || lon != 0) { // all zeros: no fix
			p = posAt(lat, lon, nil, at)
		}
	}
	name := ev.name
	if name == "" {
		name = "engine " + m[1]
	}
	s := fmt.Sprintf("%s %s %s report %s at %s", o.Airline, o.Flight, name, route(o.Origin, o.Dest), hmZ(at.Format("1504")))
	if p != nil {
		s += ": " + latlon(p.Lat, p.Lon)
	}
	return opsParsed{rep: o, pos: p, summary: strings.Join(strings.Fields(s), " "), tail: m[3]}, true
}

func parseAPM(t string) (opsParsed, bool) {
	m := apmRe.FindStringSubmatch(spaces.ReplaceAllString(strings.SplitN(t, "\n", 2)[0], " "))
	if m == nil {
		return opsParsed{}, false
	}
	at, ok := eventTime(m[6], m[7], false)
	if !ok {
		return opsParsed{}, false
	}
	o := OpsReport{Event: "APM", Origin: m[4], Dest: m[5], At: at.Format(time.RFC3339), Info: map[string]string{"report": m[1]}}
	o.setFlight(m[3], m[2])
	var p *Pos
	if g := apmPosRe.FindStringSubmatch(t); g != nil {
		lat, _ := strconv.ParseFloat(g[1], 64)
		lon, _ := strconv.ParseFloat(g[2], 64)
		var alt *float64
		if a := apmAltRe.FindStringSubmatch(t); a != nil {
			v := float64(atoi(a[1]))
			alt = &v
		}
		if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			p = posAt(lat, lon, alt, at)
		}
	}
	return opsParsed{rep: o, pos: p, summary: perfSummary(o, p, at), tail: m[2]}, true
}

// The <nnn>XEG reports. After the tag and the registration and flight come a date and time (ddmmyy hhmmss) and the route,
// in one of a few arrangements:
//
//	ER  72211026232013KDENEDDF...      a code, two digits, the date and time, the route
//	79261026005615 KDENEGLL            two digits, the date and time, the route
//	KDENLIRF281026004221  83TO451479   the route, the date and time, then digits and a code (TO: take-off)
var (
	taggedRe      = regexp.MustCompile(`^<(\d{3})>[A-Z]{3}(?: ?\d+)* (?:[A-Z] )?([A-Z0-9-]{5,6}?)([A-Z]{3}\d{1,4}) (.*)$`)
	taggedFwdRe   = regexp.MustCompile(`(?:([A-Z]{2}) )?\d{2,3}(\d{6})(\d{6}) ?([A-Z]{4})([A-Z]{4})`)
	taggedBackRe  = regexp.MustCompile(`^([A-Z]{4})([A-Z]{4})(\d{6})(\d{6})(?: +\d{1,3} ?([A-Z]{2}))?`)
	taggedSlashRe = regexp.MustCompile(`^(?:([A-Z]{2}) )?\d{0,3}(\d{2})/(\d{2})/(\d{2})(\d{2}):(\d{2}):(\d{2})([A-Z]{4})([A-Z]{4})`)
	taggedAfterRe = regexp.MustCompile(`^([A-Z]{4})([A-Z]{4}) +\d{1,3}([A-Z]{2})(\d{6})(\d{6})`)
	taggedPosRe   = regexp.MustCompile(`\b(\d{5})(-\d{6})`)
)

func parseEngineReport(t string) (opsParsed, bool) {
	m := taggedRe.FindStringSubmatch(t)
	if m == nil {
		return opsParsed{}, false
	}
	var event, date, hms, from, to string
	if b := taggedAfterRe.FindStringSubmatch(m[4]); b != nil {
		from, to, event, date, hms = b[1], b[2], b[3], b[4], b[5]
	} else if b := taggedBackRe.FindStringSubmatch(m[4]); b != nil {
		from, to, date, hms, event = b[1], b[2], b[3], b[4], b[5]
	} else if sl := taggedSlashRe.FindStringSubmatch(m[4]); sl != nil {
		event, date, hms, from, to = sl[1], sl[2]+sl[3]+sl[4], sl[5]+sl[6]+sl[7], sl[8], sl[9]
	} else if f := taggedFwdRe.FindStringSubmatch(m[4]); f != nil {
		event, date, hms, from, to = f[1], f[2], f[3], f[4], f[5]
	} else {
		return opsParsed{}, false
	}
	at, ok := eventTime(date, hms, false)
	if !ok {
		return opsParsed{}, false
	}
	o := OpsReport{Event: event, Origin: from, Dest: to, At: at.Format(time.RFC3339), Info: map[string]string{"number": m[1]}}
	if o.Event == "" {
		o.Event = "report"
	}
	if o.Origin == o.Dest { // an arrival's reports name the airport twice
		o.Origin = ""
	}
	o.setFlight(m[3], m[2])
	var p *Pos
	if g := engRepPosRe.FindStringSubmatch(t); g != nil && o.Event == "ER" {
		lat, lon := float64(atoi(strings.TrimPrefix(g[3], "-")))/1e4, float64(atoi(strings.TrimPrefix(g[4], "-")))/1e4
		if strings.HasPrefix(g[3], "-") {
			lat = -lat
		}
		if strings.HasPrefix(g[4], "-") {
			lon = -lon
		}
		alt := float64(atoi(g[1]))
		if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			p = posAt(lat, lon, &alt, at)
		}
	} else if g := taggedPosRe.FindStringSubmatch(m[4]); g != nil && o.Event != "ER" {
		lat, lon := float64(atoi(g[1]))/1e3, -float64(atoi(g[2][1:]))/1e3 // west of Greenwich only: east has no sign to go by
		if lat <= 90 && lon >= -180 {
			p = posAt(lat, lon, nil, at)
		}
	}
	if o.Event == "TO" {
		o.OffAt = o.At
	}
	return opsParsed{rep: o, pos: p, summary: perfSummary(o, p, at), tail: m[2]}, true
}

func perfSummary(o OpsReport, p *Pos, at time.Time) string {
	what := "performance report"
	if o.Event == "TO" {
		what = "take-off report"
	}
	s := fmt.Sprintf("%s %s %s %s at %s", o.Airline, o.Flight, what, route(o.Origin, o.Dest), hmZ(at.Format("1504")))
	if p != nil {
		s += ": " + latlon(p.Lat, p.Lon)
		if p.AltFt != nil {
			s += fmt.Sprintf(", FL%d", int(*p.AltFt/100))
		}
	}
	return strings.Join(strings.Fields(s), " ")
}
