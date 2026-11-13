// Package flightplan reads the flight plan and progress messages many FMS units send as ACARS label H1:
//
//	FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..N25000W170000..TUNTO..IGURU.Q13..PICHU.J7..HCN,N21557E120506FA3A
//	PRG/TS195622,301026/DTVHHH,07R,408,064946,048/FNUPS58FA3A
//
// A plan carries the flight's callsign, its airports and the route as fixes (by name, or with coordinates),
// joined by airways. Names are resolved to positions later, against a navigation database.
package flightplan

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Fix is one point of the route.
type Fix struct {
	Name   string  `json:"name"`
	Lat    float64 `json:"lat,omitempty"`
	Lon    float64 `json:"lon,omitempty"`
	HasPos bool    `json:"has_pos,omitempty"` // the message gave coordinates
	Via    string  `json:"via,omitempty"`     // the airway flown from this fix to the next, if one was named
}

// Plan is a filed flight plan.
type Plan struct {
	Callsign string `json:"callsign,omitempty"` // "UPS58", from the FN field
	From     string `json:"from"`               // ICAO
	To       string `json:"to"`
	Runway   string `json:"runway,omitempty"` // the arrival runway, where the plan gives one: KSEA(16R)
	Fixes    []Fix  `json:"fixes,omitempty"`
}

// Progress is a progress report: where the flight is headed and when it expects to arrive.
type Progress struct {
	Callsign string    `json:"callsign,omitempty"`
	At       time.Time `json:"at,omitempty"` // the report's own time (TS field)
	To       string    `json:"to,omitempty"`
	Runway   string    `json:"runway,omitempty"`
	ETA      string    `json:"eta,omitempty"`       // hhmmss UTC, except where OwnClock is set
	OwnClock bool      `json:"own_clock,omitempty"` // British Airways' reports (a /PR section): the times are on the aircraft's own clock
}

var (
	planStart = regexp.MustCompile(`FPN/(?:FN[A-Z0-9]+/)?R[IP]:DA:`)
	prgStart  = regexp.MustCompile(`^PRG/(?:TS\d{6},\d{6}/|DT[A-Z0-9]{4},)`) // Avianca sends it without the TS time
	prSec     = regexp.MustCompile(`^PR\d+,`)
	hex4      = regexp.MustCompile(`[0-9A-F]{4}$`)
	icaoRe    = regexp.MustCompile(`^[A-Z0-9]{4}$`)
	callRe    = regexp.MustCompile(`^[A-Z0-9]{3,8}$`)
	coordRe   = regexp.MustCompile(`^([NS])(\d{2}|\d{4}|\d{5})([EW])(\d{3}|\d{5}|\d{6})$`)
	airwayRe  = regexp.MustCompile(`^[A-Z]{1,3}\d{1,3}[A-Z]?$`)
	snRe      = regexp.MustCompile(`^SN`)
)

// clean drops the line breaks the sender wraps long messages with.
func clean(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(s))
}

// ParsePlan reads a flight plan message, or reports false if text is not one.
func ParsePlan(text string) (*Plan, bool) {
	text = clean(text)
	loc := planStart.FindStringIndex(text)
	if loc == nil {
		return nil, false
	}
	text = text[loc[0]+len("FPN/"):]
	secs := strings.Split(text, "/")
	// The sender glues a 4-hex-digit checksum to the end, unless a sequence number (SN...) comes last.
	if last := len(secs) - 1; !snRe.MatchString(secs[last]) {
		secs[last] = hex4.ReplaceAllString(secs[last], "")
	}
	p := &Plan{}
	var route bool
	for _, sec := range secs {
		switch {
		case strings.HasPrefix(sec, "FN") && callRe.MatchString(sec[2:]) && p.Callsign == "":
			p.Callsign = sec[2:]
		case (strings.HasPrefix(sec, "RP:") || strings.HasPrefix(sec, "RI:")) && !route:
			route = true
			parseChain(sec, p)
		}
	}
	if !route || !icaoRe.MatchString(p.From) || !icaoRe.MatchString(p.To) {
		return nil, false
	}
	return p, true
}

// ParseRI reads the route section of a plan that comes inside another report ("RI:DA:KMCO:AA:KSEA(16R):D:RDSOX1.JFRYS:F:..."), or
// reports false if it is not one.
func ParseRI(sec string) (*Plan, bool) {
	if !strings.HasPrefix(sec, "RI:DA:") && !strings.HasPrefix(sec, "RP:DA:") {
		return nil, false
	}
	p := &Plan{}
	parseChain(sec, p)
	if !icaoRe.MatchString(p.From) || !icaoRe.MatchString(p.To) {
		return nil, false
	}
	return p, true
}

// parseChain reads "RP:DA:KLAX:AA:RKSI:CR:...:D:SID:F:route:A:STAR:AP:approach:F:route": keys and values
// alternate. Only the DA/AA airports and the F (route) values are used; the departure and arrival
// procedures and the approach are left out.
func parseChain(sec string, p *Plan) {
	t := strings.Split(sec, ":")
	for i := 1; i+1 < len(t); i += 2 {
		switch t[i] {
		case "DA":
			p.From = t[i+1]
		case "AA":
			p.To = t[i+1]
			if j := strings.IndexByte(p.To, '('); j > 0 && strings.HasSuffix(p.To, ")") {
				p.To, p.Runway = p.To[:j], p.To[j+1:len(p.To)-1]
			}
		case "F":
			p.Fixes = append(p.Fixes, parseRoute(t[i+1])...)
		}
	}
}

// parseRoute splits "MCKEY..SNS.OTR13..LAPIL,N35400W120065.Q4.FUSCO" into fixes. Fixes are joined by ".."
// (direct) or, with one dot, an airway then the fix it leads to ("PEQ.Q4.FUSCO"); an airway named after
// a fix with ".." following ("SEALS.OTR13..LAPIL") is flown from that fix to the next.
func parseRoute(s string) []Fix {
	var out []Fix
	prevFix := false // the previous token was a fix, so a token after a single dot may be an airway
	for _, part := range splitDots(s) {
		tok, single := part.tok, part.single
		if tok == "" {
			continue
		}
		if single && prevFix && len(out) > 0 && airwayRe.MatchString(tok) && !strings.Contains(tok, ",") {
			out[len(out)-1].Via = tok
			prevFix = false
			continue
		}
		out = append(out, parseFix(tok))
		prevFix = true
	}
	return out
}

type dotPart struct {
	tok    string
	single bool // separated from the previous token by one dot (not two)
}

func splitDots(s string) []dotPart {
	var out []dotPart
	sep := 0
	for i := 0; ; {
		j := i
		for j < len(s) && s[j] != '.' {
			j++
		}
		out = append(out, dotPart{tok: s[i:j], single: sep == 1})
		k := j
		for k < len(s) && s[k] == '.' {
			k++
		}
		if k >= len(s) {
			return out
		}
		sep, i = k-j, k
	}
}

// parseFix reads "NAME", "NAME,N35400W120065" or "N35400W120065".
func parseFix(tok string) Fix {
	name, coords, hasComma := strings.Cut(tok, ",")
	if !hasComma {
		if lat, lon, ok := ParseCoord(tok); ok {
			return Fix{Name: tok, Lat: lat, Lon: lon, HasPos: true}
		}
		return Fix{Name: tok}
	}
	if lat, lon, ok := ParseCoord(coords); ok {
		return Fix{Name: name, Lat: lat, Lon: lon, HasPos: true}
	}
	return Fix{Name: name}
}

// ParseCoord reads "N35400W120065" (35°40.0'N 120°06.5'W), "N5130W14000" or "N51W140".
func ParseCoord(s string) (lat, lon float64, ok bool) {
	m := coordRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	deg := func(digits string, degLen int) float64 {
		d, _ := strconv.Atoi(digits[:degLen])
		v := float64(d)
		if len(digits) > degLen {
			mm, _ := strconv.Atoi(digits[degLen : degLen+2])
			v += float64(mm) / 60
			if len(digits) > degLen+2 {
				tenth, _ := strconv.Atoi(digits[degLen+2:])
				v += float64(tenth) / 600
			}
		}
		return v
	}
	lat, lon = deg(m[2], 2), deg(m[4], 3)
	if m[1] == "S" {
		lat = -lat
	}
	if m[3] == "W" {
		lon = -lon
	}
	if lat > 90 || math.Abs(lon) > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// ParseProgress reads a progress report, or reports false if text is not one.
func ParseProgress(text string) (*Progress, bool) {
	text = clean(text)
	if !prgStart.MatchString(text) {
		return nil, false
	}
	secs := strings.Split(text[len("PRG/"):], "/")
	if last := len(secs) - 1; !snRe.MatchString(secs[last]) {
		secs[last] = hex4.ReplaceAllString(secs[last], "")
	}
	p := &Progress{}
	for _, sec := range secs {
		switch {
		case strings.HasPrefix(sec, "TS"):
			f := strings.Split(sec[2:], ",")
			if len(f) == 2 {
				if t, err := time.Parse("150405 020106", f[0]+" "+f[1]); err == nil {
					p.At = t.UTC()
				}
			}
		case strings.HasPrefix(sec, "DT"):
			f := strings.Split(sec[2:], ",")
			if icaoRe.MatchString(f[0]) {
				p.To = f[0]
			}
			if len(f) > 1 {
				p.Runway = f[1]
			}
			if len(f) > 3 && len(f[3]) == 6 {
				p.ETA = f[3]
			}
		case prSec.MatchString(sec):
			p.OwnClock = true
		case prSec.MatchString(sec):
			p.OwnClock = true
		case strings.HasPrefix(sec, "FN") && callRe.MatchString(sec[2:]):
			p.Callsign = sec[2:]
		}
	}
	return p, p.To != "" || p.Callsign != ""
}

// Point is a route point with a position.
type Point struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Via  string  `json:"via,omitempty"` // the airway flown from here to the next point
	Kind string  `json:"kind"`          // "airport", "given" (coordinates in the message), "fix" (looked up by name) or "learned" (a fix name we learned the position of from the aircraft's own reports; set by the server)
}

// MaxLegNM is the longest leg a fix looked up by name may add to the route. Names repeat around the world,
// so a candidate far from the point before it is the wrong one.
const MaxLegNM = 3000

// Resolve turns a plan into positions: the departure airport, each fix, and the arrival airport. A fix with
// coordinates uses them; one without is looked up by name, and of the places with that name the one nearest the
// previous point is taken. Fixes that cannot be placed are returned in missing. lookup returns the places with
// a name as [lat, lon] pairs; airport returns an airport's position by ICAO code.
func Resolve(p *Plan, lookup func(name string) [][2]float64, airport func(code string) (lat, lon float64, ok bool)) (pts []Point, missing []string) {
	var prev *Point
	add := func(pt Point) {
		pts = append(pts, pt)
		prev = &pts[len(pts)-1]
	}
	if lat, lon, ok := airport(p.From); ok {
		add(Point{Name: p.From, Lat: lat, Lon: lon, Kind: "airport"})
	}
	for i, f := range p.Fixes {
		switch {
		case f.HasPos:
			add(Point{Name: f.Name, Lat: f.Lat, Lon: f.Lon, Via: f.Via, Kind: "given"})
			continue
		case i == len(p.Fixes)-1 && f.Name == p.To:
			continue // the arrival airport is added below
		}
		best, bestD := [2]float64{}, -1.0
		for _, c := range lookup(f.Name) {
			d := 0.0
			if prev != nil {
				d = distNM(prev.Lat, prev.Lon, c[0], c[1])
			}
			if bestD < 0 || d < bestD {
				best, bestD = c, d
			}
		}
		if bestD < 0 || bestD > MaxLegNM {
			missing = append(missing, f.Name)
			continue
		}
		add(Point{Name: f.Name, Lat: best[0], Lon: best[1], Via: f.Via, Kind: "fix"})
	}
	if lat, lon, ok := airport(p.To); ok {
		add(Point{Name: p.To, Lat: lat, Lon: lon, Kind: "airport"})
	}
	return pts, missing
}

func distNM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 3440.065
	rad := math.Pi / 180
	dlat, dlon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}
