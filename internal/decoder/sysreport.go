package decoder

import (
	"fmt"
	"regexp"
	"strings"
)

// Periodic system reports on label H1 from US aircraft (seen from N76269 and N37253), each opening with a four-letter name, the
// registration's digits and the route:
//
//	TKON76269KORDKMSP17110343 ...        takeoff, then the day, month and time of day
//	CRZN76269KORDKMSP17110413 ...        cruise
//	HYDN76269171126KORDKMSP ...          the day, month and year, then the route (a column of numbers follows)
//	PNMN76269171115362883152618790KORDMMSDCLMB ...   some digits, the route, then the phase (CLMB, CRS, DESC)
//	PNM4G941N37253111915264069111123740KSFOKPHXCLMB888807890591LBTL241   a three-letter name (PNM or CAB), a five-character code,
//	                                      the registration, month, day and time of day, some digits, the route (a CAB may have the
//	                                      origin only) and the phase (TOFF, CLMB, CRS, SCR1, SCR2, DESC)
//
// Only the header is read: the numbers after it are unexplained, so they stay in the message text. The TKON and CRZN times agree
// with when the messages were received (a CAB or PNM time, whose minutes are sometimes space-padded, is when the block was written, a few minutes before it arrives); the names HYDN and PNMN look like hydraulic and pneumatic systems but that is a guess.
var (
	sysTimedRe = regexp.MustCompile(`^(TKON|CRZN)(\d{5})([A-Z]{4})([A-Z]{4})(\d{2})(\d{2})(\d{2})(\d{2})`)
	sysHydRe   = regexp.MustCompile(`^HYDN(\d{5})(\d{2})(\d{2})(\d{2})([A-Z]{4})([A-Z]{4})`)
	sysShortRe = regexp.MustCompile(`^(PNM|CAB)([0-9A-Z]{5})N(\d{5})(\d{2})(\d{2})(\d{2})([ \d]\d)[\d ]*?([A-Z]{4})([A-Z]{4})?(TOFF|CLMB|CRS |SCR[12]|DESC)`)
	sysPneuRe  = regexp.MustCompile(`^PNMN(\d{5})[\d ]{6,30}?([A-Z]{4})([A-Z]{4})(CLMB|CRS |DESC)`)
)

func parseSystemReport(text string) (opsParsed, bool) {
	var name, digits string
	o := OpsReport{Info: map[string]string{}}
	switch {
	case sysTimedRe.MatchString(text):
		m := sysTimedRe.FindStringSubmatch(text)
		name, digits, o.Origin, o.Dest = m[1], m[2], m[3], m[4]
		o.Info["day"], o.Info["month"], o.Info["time"] = m[5], m[6], hmZ(m[7]+m[8])
		o.Phase = map[string]string{"TKON": "takeoff", "CRZN": "cruise"}[name]
	case sysHydRe.MatchString(text):
		m := sysHydRe.FindStringSubmatch(text)
		name, digits, o.Origin, o.Dest = "HYDN", m[1], m[5], m[6]
		o.Info["day"], o.Info["month"] = m[2], m[3]
	case sysShortRe.MatchString(text):
		m := sysShortRe.FindStringSubmatch(text)
		name, digits, o.Origin, o.Dest = m[1], m[3], m[8], m[9]
		o.Info["month"], o.Info["day"], o.Info["time"], o.Info["id"] = m[4], m[5], hmZ(m[6]+strings.Replace(m[7], " ", "0", 1)), m[2]
		o.Phase = map[string]string{"TOFF": "takeoff", "CLMB": "climb", "CRS ": "cruise", "SCR1": "cruise", "SCR2": "cruise", "DESC": "descent"}[m[10]]
	case sysPneuRe.MatchString(text):
		m := sysPneuRe.FindStringSubmatch(text)
		name, digits, o.Origin, o.Dest = "PNMN", m[1], m[2], m[3]
		o.Phase = map[string]string{"CLMB": "climb", "CRS ": "cruise", "DESC": "descent"}[m[4]]
	default:
		return opsParsed{}, false
	}
	if o.Origin == o.Dest && o.Dest != "" {
		return opsParsed{}, false
	}
	o.Event = name
	o.Reg = "N" + digits
	s := fmt.Sprintf("%s system report %s", name, route(o.Origin, o.Dest))
	if o.Phase != "" {
		s += ", " + o.Phase
	}
	if o.Info["time"] != "" {
		s += fmt.Sprintf(" at %s on %s/%s", o.Info["time"], o.Info["day"], o.Info["month"])
	}
	return opsParsed{rep: o, summary: s, tail: o.Reg}, true
}
