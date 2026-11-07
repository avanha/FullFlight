package decoder

import (
	"fmt"
	"html"
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
	Type     string // the aircraft type, "A350" or (for the plain text reports of the same design) "A380"
	Kind     string // REP216, MODA or PRM
	Number   string // the report number as in its name, "216" for REP216; empty for MODA and PRM
	Tail     string
	Time     time.Time // the aircraft's own time of the report (UTC), zero if it did not give one
	Phase    string    // the flight phase code the aircraft gives, as sent
	Origin   string    // ICAO, empty if the report does not know
	Dest     string
	Callsign string   // as the aircraft sends it, e.g. CPA105
	Trigger  string   // what raised the report, e.g. "INTEGRATED REFUEL PANEL DOOR OPEN"
	Params   []string // the parameters a MODA or PRM message carries, by name, each once
}

// ParseA350 reads the header of an A350 report from the text libacars (or the raw message) gives.
func ParseA350(text string) (A350Report, bool) {
	m := a350Rep.FindStringSubmatch(text)
	if m == nil {
		return A350Report{}, false
	}
	rep := A350Report{Type: "A350", Kind: m[1], Number: strings.TrimPrefix(m[1], "REP")}
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
	if rep.Kind == "MODA" || rep.Kind == "PRM" {
		rep.Params = paramNames(text)
	}
	return rep, true
}

// paramNames lists the parameters of a MODA or PRM message ("PAN3.1,8.861000E+01;PAFLEV0,1;..."), by name, each once.
func paramNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Split(text, ";")[1:] {
		name, _, ok := strings.Cut(f, ",")
		if !ok || name == "" || seen[name] || strings.ContainsAny(name, " :/") || strings.HasPrefix(name, "DS") || strings.HasPrefix(name, "RP") {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func (r A350Report) summary() string {
	typ := r.Type
	if typ == "" {
		typ = "A350"
	}
	s := typ + " " + r.Kind
	switch {
	case strings.HasPrefix(r.Kind, "REP") && r.Number != "":
		s = typ + " report " + r.Number
	case r.Kind == "MODA":
		s = typ + " monitoring data"
	case r.Kind == "PRM":
		s = typ + " parameter data"
	}
	if r.Trigger != "" {
		s += ": " + r.Trigger
	}
	if n := len(r.Params); n > 0 {
		s += fmt.Sprintf(" (%d parameters: %s)", n, listFew(r.Params, 3))
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

// listFew joins the first n of items, and says how many more there are.
func listFew(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

// lines is the report written out for reading: when it was raised, on which flight, by what.
func (r A350Report) lines() string {
	var l []string
	if r.Trigger != "" {
		l = append(l, "Raised by: "+r.Trigger)
	}
	if !r.Time.IsZero() {
		l = append(l, "Aircraft time: "+r.Time.Format("2006-01-02 15:04:05")+"Z")
	}
	if r.Callsign != "" || r.Origin != "" {
		f := r.Callsign
		if r.Origin != "" {
			f = strings.TrimSpace(f + " " + r.Origin + "→" + r.Dest)
		}
		l = append(l, "Flight: "+f)
	}
	if r.Phase != "" {
		l = append(l, "Flight phase code: "+r.Phase)
	}
	if len(r.Params) > 0 {
		l = append(l, "Parameters: "+strings.Join(r.Params, ", "))
	}
	return strings.Join(l, "\n")
}

// A380 maintenance reports are the same design sent as plain fixed-width text:
//
//	A380000150,1,1,TB000000;REP212,81;H0121281601205624.VH-OQG10061126231337020;H02YSSY FAORQFA63     S0586S0785T900200101;H03PARK BRAKE ACTIVATED RIGHT BLG   ;:
//
// H01 gives the report number, the tail and (after "10") the aircraft's date and time as ddmmyy hhmmss, then a flight phase code;
// H02 the route and the callsign (ten wide); H03 what raised the report. Read from the reports of three aircraft.
var (
	a380Rep = regexp.MustCompile(`A380\d{6},\d,\d,\w+;(REP\d+|PRM)[,;]`)
	a380H01 = regexp.MustCompile(`;H01(\d{3})\d*?\.([A-Z0-9-]+)\d{2}(\d\d)(\d\d)(\d\d)(\d\d)(\d\d)(\d\d)(\d{3});H02`)
	a380H02 = regexp.MustCompile(`;H02([A-Z]{4}|-{4}) ([A-Z]{4}|-{4})([A-Z0-9 ]{10})`)
	a380H03 = regexp.MustCompile(`;H03([^;]*)`)
)

// ParseA380 reads an A380 maintenance report or parameter message. The data lines after H03 are not read.
func ParseA380(text string) (A350Report, bool) {
	m := a380Rep.FindStringSubmatch(text)
	if m == nil {
		return A350Report{}, false
	}
	rep := A350Report{Type: "A380", Kind: m[1], Number: strings.TrimPrefix(m[1], "REP")}
	if rep.Number == rep.Kind {
		rep.Number = ""
	}
	if h := a380H01.FindStringSubmatch(text); h != nil {
		rep.Tail = h[2]
		n := func(i int) int { v, _ := strconv.Atoi(h[i]); return v }
		if t := time.Date(2000+n(5), time.Month(n(4)), n(3), n(6), n(7), n(8), 0, time.UTC); t.Day() == n(3) && n(6) < 24 && n(7) < 60 && n(8) < 60 {
			rep.Time = t
		}
		rep.Phase = h[9]
	}
	if h := a380H02.FindStringSubmatch(text); h != nil {
		if icaoR.MatchString(h[1]) && icaoR.MatchString(h[2]) {
			rep.Origin, rep.Dest = h[1], h[2]
		}
		if cs := strings.TrimSpace(h[3]); callsignR.MatchString(cs) {
			rep.Callsign = cs
		}
	}
	if h := a380H03.FindStringSubmatch(text); h != nil {
		rep.Trigger = strings.Join(strings.Fields(h[1]), " ")
	}
	if rep.Kind == "PRM" {
		rep.Params = paramNames(text)
	}
	return rep, true
}

// decodeA380 reads an A380 report sent as plain text on label H1.
func decodeA380(m Message) (Result, bool) {
	text := strings.TrimSpace(m.Text)
	rep, ok := ParseA380(text)
	if !ok {
		return Result{}, false
	}
	r := Result{Kind: KindMaint, Label: m.Label, Dir: m.Dir, Body: text, Fields: map[string]any{"a350": rep}}
	if r.Dir == Unknown {
		r.Dir = Downlink
	}
	r.Identity.Tail, r.Identity.Callsign = rep.Tail, rep.Callsign
	r.Summary = rep.summary()
	r.Text = rep.lines()
	return r, true
}

// RTRReport is the maintenance report an A350 sends as XML: <RTR><HEAD>...<HEADRTR><FROM>..<TO>..<FNBR>..
type RTRReport struct {
	Tail     string
	Time     time.Time
	Origin   string
	Dest     string
	Callsign string
	Message  string // the first fault description
	Items    []RTRItem
}

// RTRItem is one entry of an RTR report: a flight deck effect (FDE), a maintenance message that has no effect on the flight deck
// (NFDE) or a maintenance message (MM, which names the system, the ATA chapter and a priority).
type RTRItem struct {
	Kind     string // FDE, NFDE or MM
	Code     string // the message code, e.g. "3242H371"
	Text     string
	At       time.Time // zero if the entry has none
	ATA      string    // chapter, MM only
	Priority string    // MM only: low, high...
	System   string    // MM only: the system that raised it, e.g. "MMR1"
}

var (
	rtrRoot = regexp.MustCompile(`^\s*<RTR>`)
	rtrDA   = regexp.MustCompile(`<DA>(\d{4})/(\d\d)/(\d\d) (\d\d):(\d\d):(\d\d)</DA>`)
	rtrTail = regexp.MustCompile(`<ACD [^>]*tail="([A-Z0-9-]+)"`)
	rtrFrom = regexp.MustCompile(`<FROM>([A-Z]{4})</FROM>`)
	rtrTo   = regexp.MustCompile(`<TO>([A-Z]{4})</TO>`)
	rtrFnbr = regexp.MustCompile(`<FNBR>([^<]*)</FNBR>`)
	rtrMD   = regexp.MustCompile(`<MD>([^<]*)</MD>`)
	rtrItem = regexp.MustCompile(`(?s)<(FDE|NFDE|MM)\b[^>]*>(.*?)</(?:FDE|NFDE|MM)>`)
	rtrMC   = regexp.MustCompile(`<MC>([^<]*)</MC>`)
	rtrATA  = regexp.MustCompile(`<ATA>([^<]*)</ATA>`)
	rtrPrio = regexp.MustCompile(`<PRIO>([^<]*)</PRIO>`)
	rtrSys  = regexp.MustCompile(`<SYS name="([^"]*)"`)
)

func rtrTime(m []string) time.Time {
	if m == nil {
		return time.Time{}
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return time.Date(n(1), time.Month(n(2)), n(3), n(4), n(5), n(6), 0, time.UTC)
}

// ParseRTR reads an A350 maintenance report. The text may be cut short (a long report arrives in blocks); what is
// there is used.
func ParseRTR(text string) (RTRReport, bool) {
	if !rtrRoot.MatchString(text) {
		return RTRReport{}, false
	}
	var r RTRReport
	r.Time = rtrTime(rtrDA.FindStringSubmatch(text))
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
	for _, m := range rtrItem.FindAllStringSubmatch(text, -1) {
		it := RTRItem{Kind: m[1], At: rtrTime(rtrDA.FindStringSubmatch(m[2]))}
		get := func(re *regexp.Regexp) string {
			if x := re.FindStringSubmatch(m[2]); x != nil {
				return strings.Join(strings.Fields(html.UnescapeString(x[1])), " ")
			}
			return ""
		}
		it.Code, it.Text, it.Priority, it.System = get(rtrMC), get(rtrMD), get(rtrPrio), get(rtrSys)
		if it.Kind == "MM" {
			it.ATA = get(rtrATA)
		}
		r.Items = append(r.Items, it)
	}
	if len(r.Items) > 0 {
		r.Message = r.Items[0].Text
	} else if m := rtrMD.FindStringSubmatch(text); m != nil {
		r.Message = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	}
	return r, true
}

func (r RTRReport) summary() string {
	s := "A350 maintenance report"
	if r.Message != "" {
		s += ": " + r.Message
		if n := len(r.Items); n > 1 {
			s += fmt.Sprintf(" and %d more", n-1)
		}
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

// lines writes the entries out for reading, one each: what, when, which system and how urgent.
func (r RTRReport) lines() string {
	var l []string
	for _, it := range r.Items {
		s := it.Text
		if s == "" {
			s = it.Code
		}
		var more []string
		if it.Kind == "FDE" {
			more = append(more, "flight deck effect")
		}
		if it.System != "" {
			more = append(more, "system "+it.System)
		}
		if it.ATA != "" {
			more = append(more, "ATA "+it.ATA)
		}
		if it.Priority != "" {
			more = append(more, it.Priority+" priority")
		}
		if !it.At.IsZero() {
			more = append(more, it.At.Format("15:04:05")+"Z")
		}
		if len(more) > 0 {
			s += " (" + strings.Join(more, ", ") + ")"
		}
		l = append(l, s)
	}
	return strings.Join(l, "\n")
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
