package decoder

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/flightplan"
)

// Oceanic clearances over the data link, behind an address like /EGGX.OC1/ (the control centre's, or the aircraft's). The
// crew asks for a clearance, naming where the aircraft will enter the ocean and at what level and speed, and the centre
// sends back the route it may fly. A checksum of four hex digits ends the text.
//
//	/EGGX.OC1/RCL 030 / UAL26-ATSIX/1316 M085F370                                   the request: entry fix and time, Mach, level
//	/PIKCLYA.OC1/CLA 1217 261027 EGGX CLRNCE 173 / UAL26 CLRD TO KDEN VIA ATSIX     the clearance: time, date (yymmdd), centre, number,
//	  / NAT B / ATSIX 62N020W 63N030W 63N040W / 62N050W PIDSO / FM ATSIX/1316          the destination, how it enters, the track and the route
//	  MNTN F370 M085 / END OF MESSAGE                                                  from the entry fix, and the level and Mach to hold
//
// Read from samples, not a specification: one clearance and two requests. A form that does not fit is left undecoded.
type Clearance struct {
	What      string           `json:"what"` // request or clearance
	Callsign  string           `json:"callsign,omitempty"`
	Number    string           `json:"number,omitempty"` // the clearance number, or the request's
	Centre    string           `json:"centre,omitempty"` // the issuing oceanic centre, ICAO
	IssuedAt  string           `json:"issued_at,omitempty"`
	Dest      string           `json:"dest,omitempty"`
	Track     string           `json:"track,omitempty"` // NAT B
	Entry     string           `json:"entry,omitempty"` // the fix the aircraft enters the oceanic area at
	EntryTime string           `json:"entry_time,omitempty"`
	Level     int              `json:"level,omitempty"` // flight level
	Mach      float64          `json:"mach,omitempty"`
	Route     *flightplan.Plan `json:"route,omitempty"` // the fixes to fly, in order
}

var (
	ocAddrRe = regexp.MustCompile(`^/([A-Z0-9]{4,7})\.OC1/(.*)$`)
	ocReqRe  = regexp.MustCompile(`^RCL (\d+) ([A-Z0-9]+)-([A-Z0-9]+)/(\d{4}) M(\d{3})F(\d{3})$`)
	ocClaRe  = regexp.MustCompile(`^CLA (\d{4}) (\d{6}) ([A-Z]{4}) CLRNCE (\d+) ([A-Z0-9]+) CLRD TO ([A-Z]{4}) VIA ([A-Z0-9]+)(?: (NAT [A-Z]))? (.+?) FM ([A-Z0-9]+)/(\d{4}) MNTN F(\d{3}) M(\d{3}) END OF MESSAGE$`)
	ocCrcRe  = regexp.MustCompile(`[0-9A-F]{4}$`)
	ocLLRe   = regexp.MustCompile(`^(\d{2})(\d{2})?([NS])(\d{3})(\d{2})?([EW])$`)
)

func machOf(s string) float64 { return float64(atoi(s)) / 100 }

// ocFix reads a route fix: a named point, or a latitude and longitude like 62N020W (or 6230N02015W).
func ocFix(s string) flightplan.Fix {
	f := flightplan.Fix{Name: s}
	if m := ocLLRe.FindStringSubmatch(s); m != nil {
		lat := float64(atoi(m[1])) + float64(atoi(m[2]))/60
		lon := float64(atoi(m[4])) + float64(atoi(m[5]))/60
		if m[3] == "S" {
			lat = -lat
		}
		if m[6] == "W" {
			lon = -lon
		}
		if lat <= 90 && lon >= -180 && lon <= 180 {
			f.Lat, f.Lon, f.HasPos = lat, lon, true
		}
	}
	return f
}

func parseOceanic(text string) (Clearance, bool) {
	m := ocAddrRe.FindStringSubmatch(strings.Join(strings.Fields(strings.TrimSpace(text)), " "))
	if m == nil {
		return Clearance{}, false
	}
	body := ocCrcRe.ReplaceAllString(m[2], "")
	if r := ocReqRe.FindStringSubmatch(body); r != nil {
		return Clearance{What: "request", Number: r[1], Callsign: r[2], Entry: r[3], EntryTime: r[4], Mach: machOf(r[5]), Level: atoi(r[6])}, true
	}
	c := ocClaRe.FindStringSubmatch(body)
	if c == nil {
		return Clearance{}, false
	}
	issued, err := time.Parse("060102 1504", c[2]+" "+c[1])
	if err != nil {
		return Clearance{}, false
	}
	out := Clearance{What: "clearance", IssuedAt: issued.UTC().Format(time.RFC3339), Centre: c[3], Number: c[4], Callsign: c[5], Dest: c[6],
		Track: c[8], Entry: c[10], EntryTime: c[11], Level: atoi(c[12]), Mach: machOf(c[13])}
	plan := &flightplan.Plan{Callsign: c[5]}
	for _, tok := range strings.Fields(strings.ReplaceAll(c[9], "/", " ")) {
		plan.Fixes = append(plan.Fixes, ocFix(tok))
	}
	if len(plan.Fixes) == 0 {
		return Clearance{}, false
	}
	out.Route = plan
	return out, true
}

func (c Clearance) summary() string {
	level := ""
	if c.Level > 0 {
		level = fmt.Sprintf(", FL%d", c.Level)
	}
	if c.Mach > 0 {
		level += fmt.Sprintf(" M%.2f", c.Mach)
	}
	if c.What == "request" {
		return fmt.Sprintf("Oceanic clearance request %s: enter at %s %s%s", c.Callsign, c.Entry, hmZ(c.EntryTime), level)
	}
	names := make([]string, len(c.Route.Fixes))
	for i, f := range c.Route.Fixes {
		names[i] = f.Name
	}
	track := ""
	if c.Track != "" {
		track = c.Track + ", "
	}
	return fmt.Sprintf("Oceanic clearance %s to %s: %s%s (entry %s %s%s)", c.Callsign, c.Dest, track, strings.Join(names, " "), c.Entry, hmZ(c.EntryTime), level)
}

func decodeOceanic(m Message) (Result, bool) {
	prefix, body := SplitPrefix(strings.TrimSpace(m.Text))
	c, ok := parseOceanic(body)
	if !ok {
		if c, ok = parseOceanic(strings.TrimSpace(m.Text)); !ok {
			return Result{}, false
		}
		prefix, body = "", m.Text
	}
	r := Result{Kind: KindClearance, Label: m.Label, Dir: m.Dir, Prefix: prefix, Body: strings.TrimSpace(body), Fields: map[string]any{"clearance": c}, Identity: parsePrefix(prefix)}
	if r.Dir == Unknown {
		r.Dir = Uplink
		if c.What == "request" {
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
