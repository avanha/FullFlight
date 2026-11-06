package decoder

import (
	"fmt"
	"regexp"
	"strings"
)

// EVA Air's movement messages, labels 11 (out), 12 (off), 13 (on) and 14 (in), read from B-16783's:
//
//	OFF01EVA619    /--310633KLAXRCTP        wheels up on the 31st at 06:33Z, Los Angeles to Taipei
//	M39ABR6312OUT01EVA6312   /--010148ZSAMRCTP   (behind a message number and the flight again)
//
// The event, two digits, the flight, then the day of the month, the time and the route. An arrival names its airport twice.
var evaMoveRe = regexp.MustCompile(`(OUT|OFF|ONN|INN)\d{2}(EVA)(\d{1,4}) +/--(\d{2})(\d{4})([A-Z]{4})([A-Z]{4})`)

func parseEVAMovement(text string) (opsParsed, bool) {
	m := evaMoveRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil || hhmm(m[5]) == "" {
		return opsParsed{}, false
	}
	o := OpsReport{Event: m[1], Origin: m[6], Dest: m[7], Info: map[string]string{"day": m[4], "time": m[5][:2] + ":" + m[5][2:] + "Z"}}
	if o.Origin == o.Dest { // an arrival's reports name the airport twice
		o.Origin = ""
	}
	o.Callsign, o.Flight = opsFlight(m[2], m[3])
	o.Airline = opsAirlineOf(o.Callsign)
	verb := ""
	switch m[1] {
	case "OUT":
		o.Out, verb = m[5], "left the gate"
	case "OFF":
		o.Off, verb = m[5], "took off"
	case "ONN":
		o.On, verb = m[5], "landed"
	default:
		o.In, verb = m[5], "reached the gate"
	}
	return opsParsed{rep: o, summary: strings.TrimSpace(fmt.Sprintf("%s %s %s %s at %s", reportStart(o), o.name(), verb, route(o.Origin, o.Dest), hmZ(m[5])))}, true
}
