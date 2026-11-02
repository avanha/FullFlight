package decoder

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/avanha/fullflight/internal/flightplan"
)

// Qantas pre-departure clearances over the VHF data link (label RA), sent to the aircraft at the gate. The text is a clearance
// for one flight: when it was issued (day and time), the flight, its type, origin and departure time, the destination, the
// runway and departure procedure, the route to fly, the first altitude, the departure frequency and the transponder code, and
// at times a remark. The route is airways and fixes joined by DCT (direct) and, over the ocean, latitudes and longitudes in
// degrees and minutes (4219S14239E). A lone T ends it; the routes seen stop short of the destination.
//
//	.HDQOXQF ~1 PDC UPLINK
//	PDC 012148                                       issued on the 1st at 21:48
//	QFA63 A388 YSSY 2230                             callsign, type, origin, departure time
//	CLEARED TO FAOR VIA
//	34L TONTO1 DEP NWA:TRAN                          runway, departure procedure, its transition
//	ROUTE:DCT WOL H20 OTKED DCT 3846S14812E 4219S14239E ... 5919S09210E T
//	CLIMB VIA SID TO: 5000
//	DEP FREQ: 129.700
//	SQUAWK 1246
//	..
//
// The aircraft answers with a short message that repeats the first line. Read from six samples; a form that does not fit is left
// undecoded.
type DepartureClearance struct {
	Day         string           `json:"day,omitempty"`  // of the month it was issued, "01"
	Time        string           `json:"time,omitempty"` // and the time of day, "21:48"
	Callsign    string           `json:"callsign,omitempty"`
	Type        string           `json:"type,omitempty"`
	Origin      string           `json:"origin,omitempty"`
	Departure   string           `json:"departure,omitempty"` // the departure time, "2230"
	Dest        string           `json:"dest,omitempty"`
	Runway      string           `json:"runway,omitempty"`
	SID         string           `json:"sid,omitempty"`
	Transition  string           `json:"transition,omitempty"`
	ClimbTo     int              `json:"climb_to,omitempty"` // feet
	DepFreq     string           `json:"dep_freq,omitempty"`
	Squawk      string           `json:"squawk,omitempty"`
	Remark      string           `json:"remark,omitempty"`
	Route       *flightplan.Plan `json:"route,omitempty"`
	Acknowledge bool             `json:"acknowledge,omitempty"` // the aircraft's reply, which has nothing else in it
}

var (
	pdcRe = regexp.MustCompile(`^(?:\.\S+ )?~1 PDC UPLINK PDC (\d{2})(\d{2})(\d{2}) ([A-Z0-9]+) ([A-Z0-9]+) ([A-Z]{4}) (\d{4}) CLEARED TO ([A-Z]{4}) VIA (.+?) ROUTE: ?(.+?) CLIMB VIA SID TO: (\d+) DEP FREQ: ([\d.]+) SQUAWK (\d{4})(.*)$`)
	// the reply: the header with a slash before the ~
	pdcAckRe = regexp.MustCompile(`^(?:\.\S+ )?/~1 PDC UPLINK$`)
	pdcSIDRe = regexp.MustCompile(`^(\d{2}[LRC]?) (.+?) DEP:?(?: ([A-Z0-9]+?)(:TRAN)?)?$`)
	airwayRe = regexp.MustCompile(`^[A-Z]{1,2}\d{1,3}$`)
)

func parsePDC(text string) (DepartureClearance, bool) {
	flat := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if pdcAckRe.MatchString(flat) {
		return DepartureClearance{Acknowledge: true}, true
	}
	m := pdcRe.FindStringSubmatch(flat)
	if m == nil {
		return DepartureClearance{}, false
	}
	if atoi(m[2]) > 23 || atoi(m[3]) > 59 || atoi(m[1]) < 1 || atoi(m[1]) > 31 {
		return DepartureClearance{}, false
	}
	c := DepartureClearance{Day: m[1], Time: m[2] + ":" + m[3], Callsign: m[4], Type: m[5], Origin: m[6], Departure: m[7], Dest: m[8],
		ClimbTo: atoi(m[11]), DepFreq: m[12], Squawk: m[13]}
	if s := pdcSIDRe.FindStringSubmatch(m[9]); s != nil {
		c.Runway, c.SID = s[1], s[2]
		if s[4] != "" { // "XXX" alone means no transition
			c.Transition = s[3]
		}
	} else {
		return DepartureClearance{}, false
	}
	c.Remark = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[14]), ".."))
	plan := &flightplan.Plan{Callsign: c.Callsign, From: c.Origin}
	via := ""
	for _, tok := range strings.Fields(strings.TrimSuffix(" "+m[10], " T")) {
		switch {
		case tok == "DCT":
			via = ""
		case airwayRe.MatchString(tok):
			via = tok
		default:
			f := ocFix(tok)
			f.Via, via = via, ""
			plan.Fixes = append(plan.Fixes, f)
		}
	}
	if len(plan.Fixes) == 0 {
		return DepartureClearance{}, false
	}
	c.Route = plan
	return c, true
}

func (c DepartureClearance) summary() string {
	if c.Acknowledge {
		return "Reply to the pre-departure clearance"
	}
	s := fmt.Sprintf("Pre-departure clearance %s %s → %s: runway %s, %s", c.Callsign, c.Origin, c.Dest, c.Runway, c.SID)
	if c.Transition != "" {
		s += " (" + c.Transition + ")"
	}
	names := make([]string, len(c.Route.Fixes))
	for i, f := range c.Route.Fixes {
		names[i] = f.Name
	}
	s += ", " + strings.Join(names, " ")
	if c.Squawk != "" {
		s += ", squawk " + c.Squawk
	}
	return s
}

func decodePDC(m Message) (Result, bool) {
	c, ok := parsePDC(m.Text)
	if !ok {
		return Result{}, false
	}
	r := Result{Kind: KindPDC, Label: m.Label, Dir: m.Dir, Body: strings.TrimSpace(m.Text), Fields: map[string]any{"pdc": c}}
	if r.Dir == Unknown {
		r.Dir = Uplink
		if c.Acknowledge {
			r.Dir = Downlink
		}
	}
	if c.Callsign != "" {
		id := callsignIdentity(c.Callsign)
		r.Identity.Callsign, r.Identity.Flight = id.Callsign, id.Flight
	}
	r.Summary = c.summary()
	r.Text = r.Summary
	return r, true
}
