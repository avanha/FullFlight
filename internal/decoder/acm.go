package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The aircraft condition monitoring report, label H1, that Air France, KLM, LATAM, China Cargo, EVA Air, United and Air Canada aircraft send
// (A350, A330, 777, 787 and A321 among them):
//
//	ACM01ACM211BSF-GSQL  AFR98E  LFPGKIAH131126  67TR120825148223112ANTACM40
//	ACM01ACM300BSB-223A  CKK215  ZSPDEDDF241026  70ER04391867912310ACKKSGY17
//
// After the report number (ACM211) and a letter come an S, the registration, the flight, the origin and destination, the date
// (DDMMYY), a number, a two-letter code (TR, ER, PO, CL, TA ... whose meaning is not given) and the time of day (HHMMSS UTC). Then
// come numbers and text whose meaning is not given: only the header is read. The time is the event's, and these reports are often
// delivered hours or days late (the median was four hours, the longest a week), so it is the time they count from. Read from 215
// messages of seven airlines.
var acmRe = regexp.MustCompile(`^ACM01ACM(\d{3})([A-Z])S([A-Z0-9-]{4,8}?)\s+([A-Z]{3})(\d{1,4}[A-Z]?)\s+([A-Z]{4})([A-Z]{4})(\d{6})\s*(\d{1,4})([A-Z]{2})(\d{6})`)

var acmRegRe = regexp.MustCompile(`^[A-Z]{1,2}-[A-Z0-9]{3,5}$`)

// acmReg tidies the registration field: a stray letter in front of a hyphenated registration of seven characters (APH-BQI) is dropped.
func acmReg(s string) string {
	if len(s) == 7 && s[0] == 'A' && acmRegRe.MatchString(s[1:]) {
		return s[1:]
	}
	return s
}

func parseACM(text string) (opsParsed, bool) {
	m := acmRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Event: "ACM" + m[1], Origin: icaoOf(m[6]), Dest: icaoOf(m[7]), Reg: acmReg(m[3]),
		Info: map[string]string{"type": m[2], "code": m[10], "number": strings.TrimSpace(m[9])}}
	if o.Origin == o.Dest { // a report made on the ground says EGLL to EGLL
		o.Origin, o.Dest = "", ""
	}
	digits := strings.TrimRight(m[5], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") // AFR98E is flight 98 with a letter
	if n, err := strconv.Atoi(digits); err == nil && n > 0 {
		icao, iata := opsFlight(m[4], digits)
		o.Callsign, o.Flight = icao+m[5][len(digits):], iata+m[5][len(digits):]
		o.Airline = opsAirline(m[4])
	}
	if at, ok := eventTime(m[8], m[11], false); ok {
		o.At = at.Format("2006-01-02T15:04:05Z07:00")
	}
	who := o.Airline
	if who == "" {
		who = "Aircraft"
	}
	s := fmt.Sprintf("%s condition monitoring report %s", who, m[1])
	if o.Callsign != "" {
		s += " " + o.Callsign
	}
	if r := route(o.Origin, o.Dest); r != "" {
		s += " " + r
	}
	if len(m[11]) == 6 {
		s += fmt.Sprintf(" at %s:%sZ", m[11][:2], m[11][2:4])
	}
	extra := []string{fmt.Sprintf("Event code %s; the numbers that follow are not decoded", m[10])}
	return opsParsed{rep: o, summary: s, extra: extra, tail: o.Reg}, true
}
