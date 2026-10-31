package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Airbus A350 sends its ACMS and maintenance reports as MIAM messages (usually compressed). What they say about the
// aircraft's systems is beyond us, but each opens with a header that names the aircraft, the flight, the route and the
// moment, which is what a tracker wants.

var (
	// A350,<n>,1,1,TB000000/REP216,82,01;H01,216,82,01,5070,06622,.9V-SGC,3,0,26,10,26,03,28,14,071/H02,KJFK WSSS,SIA23     ,S0385,S0685,2511040901/H03,<trigger>/...
	a350Rep   = regexp.MustCompile(`A350,\d+,\d+,\d+,TB\w+/(REP\d+|MODA|PRM)[,;]`)
	a350H01   = regexp.MustCompile(`[;/]H01,(\d+),[^,]*,[^,]*,\d+,\d+,([.A-Z0-9-]+),\d,\d,(\d\d),(\d\d),(\d\d),(\d\d),(\d\d),(\d\d),(\d+)`)
	a350H02   = regexp.MustCompile(`/H02,([A-Z]{4}|-+) ([A-Z]{4}|-+),([^,]*),`)
	a350H03   = regexp.MustCompile(`/H03,([^/]*)`)
	callsignR = regexp.MustCompile(`^[A-Z]{2,3}\d{1,4}[A-Z]?$|^[A-Z0-9]{2}\d{1,4}[A-Z]?$`)
	icaoR     = regexp.MustCompile(`^[A-Z]{4}$`)
)

// A350Report is the header of an A350 report.
type A350Report struct {
	Kind     string // REP216, MODA or PRM
	Number   string // the report number as in its name, "216" for REP216; empty for MODA and PRM
	Tail     string
	Time     time.Time // the aircraft's own time of the report (UTC), zero if it did not give one
	Phase    string    // the flight phase code the aircraft gives, as sent
	Origin   string    // ICAO, empty if the report does not know
	Dest     string
	Callsign string // as the aircraft sends it, e.g. CPA105
	Trigger  string // what raised the report, e.g. "INTEGRATED REFUEL PANEL DOOR OPEN"
}

// ParseA350 reads the header of an A350 report from the text libacars (or the raw message) gives.
func ParseA350(text string) (A350Report, bool) {
	m := a350Rep.FindStringSubmatch(text)
	if m == nil {
		return A350Report{}, false
	}
	rep := A350Report{Kind: m[1], Number: strings.TrimPrefix(m[1], "REP")}
	if rep.Number == rep.Kind {
		rep.Number = ""
	}
	if h := a350H01.FindStringSubmatch(text); h != nil {
		if tail := strings.TrimLeft(h[2], "."); tail != "" && !strings.HasPrefix(tail, "XXX") {
			rep.Tail = tail
		}
		n := func(i int) int { v, _ := strconv.Atoi(h[i]); return v }
		if t := time.Date(2000+n(5), time.Month(n(4)), n(3), n(6), n(7), n(8), 0, time.UTC); t.Day() == n(3) && n(6) < 24 {
			rep.Time = t
		}
		rep.Phase = h[9]
	}
	if h := a350H02.FindStringSubmatch(text); h != nil {
		if icaoR.MatchString(h[1]) && icaoR.MatchString(h[2]) {
			rep.Origin, rep.Dest = h[1], h[2]
		}
		if cs := strings.TrimSpace(h[3]); callsignR.MatchString(cs) {
			rep.Callsign = cs
		}
	}
	if h := a350H03.FindStringSubmatch(text); h != nil {
		rep.Trigger = strings.Join(strings.Fields(h[1]), " ")
	}
	return rep, true
}

func (r A350Report) summary() string {
	s := "A350 " + r.Kind
	switch {
	case strings.HasPrefix(r.Kind, "REP") && r.Number != "":
		s = "A350 report " + r.Number
	case r.Kind == "MODA":
		s = "A350 monitoring data"
	case r.Kind == "PRM":
		s = "A350 parameter data"
	}
	if r.Trigger != "" {
		s += ": " + r.Trigger
	}
	if r.Callsign != "" || r.Origin != "" {
		var who []string
		if r.Callsign != "" {
			who = append(who, r.Callsign)
		}
		if r.Origin != "" {
			who = append(who, r.Origin+"→"+r.Dest)
		}
		s += " (" + strings.Join(who, " ") + ")"
	}
	return s
}

// RTRReport is the maintenance report an A350 sends as XML: <RTR><HEAD>...<HEADRTR><FROM>..<TO>..<FNBR>..
type RTRReport struct {
	Tail     string
	Time     time.Time
	Origin   string
	Dest     string
	Callsign string
	Message  string // the first fault description
}

var (
	rtrRoot = regexp.MustCompile(`^\s*<RTR>`)
	rtrDA   = regexp.MustCompile(`<DA>(\d{4})/(\d\d)/(\d\d) (\d\d):(\d\d):(\d\d)</DA>`)
	rtrTail = regexp.MustCompile(`<ACD [^>]*tail="([A-Z0-9-]+)"`)
	rtrFrom = regexp.MustCompile(`<FROM>([A-Z]{4})</FROM>`)
	rtrTo   = regexp.MustCompile(`<TO>([A-Z]{4})</TO>`)
	rtrFnbr = regexp.MustCompile(`<FNBR>([^<]*)</FNBR>`)
	rtrMD   = regexp.MustCompile(`<MD>([^<]*)</MD>`)
)

// ParseRTR reads an A350 maintenance report. The text may be cut short (a long report arrives in blocks); what is
// there is used.
func ParseRTR(text string) (RTRReport, bool) {
	if !rtrRoot.MatchString(text) {
		return RTRReport{}, false
	}
	var r RTRReport
	if m := rtrDA.FindStringSubmatch(text); m != nil {
		n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
		r.Time = time.Date(n(1), time.Month(n(2)), n(3), n(4), n(5), n(6), 0, time.UTC)
	}
	if m := rtrTail.FindStringSubmatch(text); m != nil {
		r.Tail = m[1]
	}
	if m := rtrFrom.FindStringSubmatch(text); m != nil {
		r.Origin = m[1]
	}
	if m := rtrTo.FindStringSubmatch(text); m != nil {
		r.Dest = m[1]
	}
	if m := rtrFnbr.FindStringSubmatch(text); m != nil {
		if cs := strings.TrimSpace(m[1]); callsignR.MatchString(cs) {
			r.Callsign = cs
		}
	}
	if m := rtrMD.FindStringSubmatch(text); m != nil {
		r.Message = strings.Join(strings.Fields(m[1]), " ")
	}
	return r, true
}

func (r RTRReport) summary() string {
	s := "A350 maintenance report"
	if r.Message != "" {
		s += ": " + r.Message
	}
	if r.Callsign != "" || r.Origin != "" {
		var who []string
		if r.Callsign != "" {
			who = append(who, r.Callsign)
		}
		if r.Origin != "" && r.Dest != "" {
			who = append(who, r.Origin+"→"+r.Dest)
		}
		s += " (" + strings.Join(who, " ") + ")"
	}
	return s
}

// TakeoffReport is the takeoff report some airlines' aircraft send ("3S06 TAKOFF  0021 /28  KEWR/ WSSS  .9V-SGC  28102026 1558
// /ATF 116.7/TOP ----/ZFW 158000/TOF 2/TOW 273552/THS -07.2/TOG 25.5/V1 153/VR 153/V2 159/OUT 1539/OFF 1556/ETA 1017/TRW 22R").
type TakeoffReport struct {
	Number   string // the flight number, no leading zeros
	Origin   string
	Dest     string
	Tail     string
	Out, Off string // hhmm UTC, as sent; empty if absent
	ETA      string
	Runway   string
	TOWkg    int
}

var (
	takeoffHead = regexp.MustCompile(`^\w{2,4}\s+TAKOFF\s+(\d{1,4}) */\d+\s+([A-Z]{4})/ ?([A-Z]{4})\s+\.?([A-Z0-9-]+)\s+\d{8} \d{4}`)
	takeoffKV   = regexp.MustCompile(`/(OUT|OFF|ETA|TRW|TOW)\s+([A-Z0-9]+)`)
)

// ParseTakeoff reads a takeoff report.
func ParseTakeoff(text string) (TakeoffReport, bool) {
	m := takeoffHead.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return TakeoffReport{}, false
	}
	r := TakeoffReport{Number: strings.TrimLeft(m[1], "0"), Origin: m[2], Dest: m[3], Tail: m[4]}
	for _, kv := range takeoffKV.FindAllStringSubmatch(text, -1) {
		switch kv[1] {
		case "OUT":
			r.Out = kv[2]
		case "OFF":
			r.Off = kv[2]
		case "ETA":
			r.ETA = kv[2]
		case "TRW":
			r.Runway = kv[2]
		case "TOW":
			r.TOWkg, _ = strconv.Atoi(kv[2])
		}
	}
	return r, true
}

func (r TakeoffReport) summary() string {
	hm := func(s string) string {
		if len(s) == 4 {
			return s[:2] + ":" + s[2:] + "Z"
		}
		return s
	}
	s := fmt.Sprintf("Takeoff report %s→%s", r.Origin, r.Dest)
	if r.Off != "" {
		s += " at " + hm(r.Off)
	}
	if r.Runway != "" {
		s += ", runway " + r.Runway
	}
	if r.ETA != "" {
		s += ", ETA " + hm(r.ETA)
	}
	return s
}
