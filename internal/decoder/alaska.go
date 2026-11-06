package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Alaska Airlines' operations messages, read from the traffic of N956AK and checked against its track. The airline writes its
// flight numbers without a prefix, so a flight is "AS" and the number; the callsign is only known where a report spells it out.

// The movement messages on label 80, one a transition of the flight:
//
//	1101 OFFRP  1388/31 KBUR/KSEA .N956AK        wheels up
//	/OUT 1744/OFF 1804/FOB 0172/ETA 2004/CAB 1739/...
//	1201 ONRP   0670/01 KLAX/KPDX .N956AK        wheels down
//	/ON  2016/FOB 0087
//	3701 INRANG 1614/31 KSEA/KLAX .N956AK        within range of the destination, with a new ETA
//	/ETA 0154
//	1701 FAROUT 0649/01 KPDX/KBUR .N956AK        away from the gate
//	/OUT 2307/FAROUT 230947/FOB 0156
//
// The flight number, then the day of the month, the route (the destination is blank until it is known), the tail and the times
// as hhmm UTC. Others on the label (door and cargo reports, acknowledgements) say nothing about the flight and are left alone.
var (
	akMoveRe = regexp.MustCompile(`^\w{4} (OFFRP|ONRP|INRANG|FAROUT) +(\d{4})/(\d{2}) ([A-Z]{4})/([A-Z]{4})? *\.([A-Z0-9-]+)(.*)$`)
	akTimeRe = regexp.MustCompile(`/(OUT|OFF|ON|ETA) +(\d{4})`)
)

var akMoveWords = map[string]string{"OFFRP": "takeoff report", "ONRP": "landing report", "INRANG": "arrival estimate", "FAROUT": "pushback report"}

func parseAlaskaMovement(text string) (opsParsed, bool) {
	t := spaces.ReplaceAllString(strings.ReplaceAll(strings.TrimSpace(text), "\r", ""), " ")
	m := akMoveRe.FindStringSubmatch(t)
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Alaska", Event: m[1], Origin: m[4], Dest: m[5], Reg: m[6], Info: map[string]string{"day": m[3]}}
	o.Flight = alaskaFlight(m[2])
	for _, g := range akTimeRe.FindAllStringSubmatch(m[7], -1) {
		v := hhmm(g[2])
		if v == "" {
			continue
		}
		switch g[1] {
		case "OUT":
			o.Out = v
		case "OFF":
			o.Off = v
		case "ON":
			o.On = v
		case "ETA":
			o.ETA = v
		}
	}
	s := strings.TrimSpace(fmt.Sprintf("Alaska %s %s %s", o.Flight, akMoveWords[m[1]], route(o.Origin, o.Dest)))
	if tm := o.times(); tm != "" {
		s += ": " + tm
	}
	return opsParsed{rep: o, summary: s, tail: o.Reg}, true
}

func alaskaFlight(digits string) string {
	n, err := strconv.Atoi(digits)
	if err != nil || n == 0 {
		return ""
	}
	return "AS" + strconv.Itoa(n)
}

// The takeoff report on label H1, a few lines of engine and system numbers after the first two:
//
//	B44B
//	N956AK    21388KBURKSEATO311026180429
//
// The tail, a digit (the leg of the day), the flight, the route, then TO and the takeoff as ddmmyy and hhmmss UTC.
var akTakeoffRe = regexp.MustCompile(`^\w{4}\s+([A-Z0-9-]+) +\d(\d{4})([A-Z]{4})([A-Z]{4})TO(\d{6})(\d{6})`)

func parseAlaskaTakeoff(text string) (opsParsed, bool) {
	m := akTakeoffRe.FindStringSubmatch(strings.ReplaceAll(strings.TrimSpace(text), "\r", ""))
	if m == nil {
		return opsParsed{}, false
	}
	at, ok := eventTime(m[5], m[6], false)
	if !ok {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Alaska", Event: "TO", Origin: m[3], Dest: m[4], Reg: m[1], Flight: alaskaFlight(m[2]), At: at.Format(time.RFC3339), OffAt: at.Format(time.RFC3339)}
	s := fmt.Sprintf("Alaska %s takeoff report %s: off %s", o.Flight, route(o.Origin, o.Dest), at.Format("15:04:05Z"))
	return opsParsed{rep: o, summary: s, tail: o.Reg}, true
}

// The position report that starts ABS (ABS001DA_ N956AK KSEAKMCO, then a line for each position), a cousin of EB with its
// fields a different width:
//
//	ABS001DA_N956AK  KSEAKMCO271
//	 46962-1197061642030855-483278032XA0/9W0190230000--
//
// Latitude and longitude in thousandths of a degree (the longitude's minus sign is what separates them), the time hhmm, the
// altitude in six digits, the temperature in whole degrees with its sign, then a digit and the wind direction and speed. The
// header may be missing from a later block of a long message, which then carries only the lines and is not read.
var (
	absHeadRe = regexp.MustCompile(`^ABS\d{3}[A-Z]{2}_ *([A-Z0-9-]+) +([A-Z]{4})([A-Z]{4})\d*\s*(?:\n|$)`)
	absLineRe = regexp.MustCompile(`^\s*(\d{4,5}) *-(\d{11,16})([-+ ]\d{2}).([ \d]{3})(\d{3})`)
)

func parseABS(text string) (opsParsed, bool) {
	t := strings.ReplaceAll(strings.TrimSpace(text), "\r", "")
	h := absHeadRe.FindStringSubmatch(t)
	if h == nil {
		return opsParsed{}, false
	}
	var fixes []ebFix
	for _, line := range strings.Split(t, "\n")[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := absLineRe.FindStringSubmatch(line)
		if m == nil {
			return opsParsed{}, false
		}
		run := m[2] // the longitude, then the time (4) and the altitude (6)
		cut := len(run) - 10
		secs, ok := daySecs(atoi(run[cut:cut+2]), atoi(run[cut+2:cut+4]), 0)
		f := ebFix{lat: atof(m[1]) / 1000, lon: -atof(run[:cut]) / 1000, alt: atof(run[cut+4:]), temp: atof(strings.Replace(m[3], " ", "+", 1)),
			dir: atof(strings.TrimSpace(m[4])), kt: atof(m[5]), secs: secs}
		if !ok || f.lat > 90 || f.lon < -180 {
			return opsParsed{}, false
		}
		fixes = append(fixes, f)
	}
	if len(fixes) == 0 {
		return opsParsed{}, false
	}
	return ebResult(h[1], h[2], h[3], "ABS", "Alaska", fixes), true
}
