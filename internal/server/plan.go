package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airports"
	"github.com/avanha/fullflight/internal/decoder"
	"github.com/avanha/fullflight/internal/flightplan"
	"github.com/avanha/fullflight/internal/geo"
)

// planJSON is a filed flight plan resolved to positions, for drawing and projecting along.
type planJSON struct {
	ID       int64              `json:"id"`
	T        time.Time          `json:"t"` // when the message arrived
	Callsign string             `json:"callsign,omitempty"`
	From     string             `json:"from"`
	To       string             `json:"to"`
	FromName string             `json:"from_name,omitempty"`
	ToName   string             `json:"to_name,omitempty"`
	Points   []flightplan.Point `json:"points"`
	Missing  []string           `json:"missing,omitempty"` // fixes whose position we could not find
	Kind     string             `json:"kind,omitempty"`    // "clearance": an oceanic clearance, the route over the ocean only, not a flight plan; "pdc": a pre-departure clearance
	Detail   string             `json:"detail,omitempty"`  // a clearance's track, level and entry
}

// plans serves the flight plans an aircraft sent, oldest first. A plan is often sent several times (heard by
// more than one station, or re-sent when the crew changes it); identical ones are reported once, at the
// time of the newest. Fixes named without coordinates are placed from what we have learned of them, else from the
// navigation database.
func (s *Server) plans(w http.ResponseWriter, r *http.Request) {
	hex := strings.ToUpper(r.PathValue("hex"))
	since, until := s.window(r, 96, 24*30)
	ms, err := s.Store.Messages(hex, since, until, 2000)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	// A fix name's positions: what we have learned from aircraft's own reports if anything (it is current, and
	// covers names the 2013 database lacks), else the navigation database.
	learned := map[string][][2]float64{}
	lookup := func(name string) [][2]float64 {
		var out [][2]float64
		for _, l := range s.Store.LearnedFixes(name) {
			out = append(out, [2]float64{l.Lat, l.Lon})
		}
		if len(out) > 0 {
			learned[name] = out
			return out
		}
		for _, p := range s.Nav.Lookup(name) {
			if s.Store.SuspectFix(name, p.Lat, p.Lon) {
				continue // a flight plan showed the database wrong about it
			}
			out = append(out, [2]float64{p.Lat, p.Lon})
		}
		return out
	}
	airport := func(code string) (float64, float64, bool) {
		a, ok := airports.Lookup(code)
		return a.Lat, a.Lon, ok
	}
	out := []planJSON{}
	seen := map[string]int{}  // identical plans: index of the one kept
	given := map[string]int{} // how many of its fixes came with coordinates (a station may have heard only part of a message)
	for _, m := range ms {    // oldest first
		if m.Kind == "oceanic_clearance" {
			var d struct {
				Fields struct {
					Clearance *decoder.Clearance `json:"clearance"`
				} `json:"fields"`
			}
			if json.Unmarshal([]byte(m.Decoded), &d) != nil || d.Fields.Clearance == nil {
				continue
			}
			c := d.Fields.Clearance
			if c.What != "clearance" || c.Route == nil {
				continue
			}
			pts, missing := flightplan.Resolve(c.Route, lookup, airport) // no origin or destination: just the fixes
			key := "clearance|" + c.Callsign + "|" + routeKey(c.Route)
			if k, dup := seen[key]; dup {
				out[k].T, out[k].ID = m.T, m.ID
				continue
			}
			detail := c.Track
			if detail != "" {
				detail += " · "
			}
			detail += fmt.Sprintf("FL%d M%.2f · entry %s %s", c.Level, c.Mach, c.Entry, c.EntryTime)
			seen[key] = len(out)
			out = append(out, planJSON{ID: m.ID, T: m.T, Callsign: c.Callsign, To: c.Dest, Points: pts, Missing: missing, Kind: "clearance", Detail: detail})
			continue
		}
		if m.Kind == "departure_clearance" {
			var d struct {
				Fields struct {
					PDC *decoder.DepartureClearance `json:"pdc"`
				} `json:"fields"`
			}
			if json.Unmarshal([]byte(m.Decoded), &d) != nil || d.Fields.PDC == nil || d.Fields.PDC.Route == nil {
				continue
			}
			c := d.Fields.PDC
			route := *c.Route // from the origin airport, to no airport: the clearance stops short of the destination
			route.From = c.Origin
			pts, missing := flightplan.Resolve(&route, lookup, airport)
			pts, missing, _ = dropDetours(pts, missing)
			at := m.At // issued on its day and time, whenever it was delivered
			if at.IsZero() {
				at = m.T
			}
			key := "pdc|" + c.Callsign + "|" + c.Origin + "|" + routeKey(c.Route)
			if k, dup := seen[key]; dup {
				out[k].T, out[k].ID = at, m.ID
				continue
			}
			detail := fmt.Sprintf("runway %s, %s", c.Runway, c.SID)
			if c.Transition != "" {
				detail += " (" + c.Transition + ")"
			}
			detail += fmt.Sprintf(" \u00b7 climb %d ft \u00b7 departure %s \u00b7 squawk %s \u00b7 issued on day %s at %sZ", c.ClimbTo, c.DepFreq, c.Squawk, c.Day, c.Time)
			if c.Remark != "" {
				detail += " \u00b7 " + strings.ToLower(c.Remark)
			}
			seen[key] = len(out)
			out = append(out, planJSON{ID: m.ID, T: at, Callsign: c.Callsign, From: c.Origin, To: c.Dest, Points: pts, Missing: missing, Kind: "pdc", Detail: detail})
			continue
		}
		if m.Kind != "flight_plan" && m.Kind != "fms_position" { // (an FMS report carries its plan)
			continue
		}
		var d struct {
			Fields struct {
				Plan *flightplan.Plan `json:"plan"`
			} `json:"fields"`
		}
		if json.Unmarshal([]byte(m.Decoded), &d) != nil || d.Fields.Plan == nil {
			continue
		}
		p := d.Fields.Plan
		key := p.Callsign + "|" + p.From + "|" + p.To + "|" + routeKey(p)
		pts, missing := flightplan.Resolve(p, lookup, airport)
		for i := range pts {
			if pts[i].Kind == "fix" && len(learned[pts[i].Name]) > 0 {
				pts[i].Kind = "learned"
			}
		}
		// A learned position is the aircraft's own; one from the database that sends the route far off and back is the wrong place of that
		// name, and is remembered as such.
		var dropped []flightplan.Point
		pts, missing, dropped = dropDetours(pts, missing)
		for _, d := range dropped {
			s.Store.MarkFixSuspect(d.Name, d.Lat, d.Lon, fmt.Sprintf("sent the route of the flight plan of %s %s\u2192%s far off its course", p.Callsign, p.From, p.To), m.T)
		}
		if k, dup := seen[key]; dup {
			out[k].T, out[k].ID = m.T, m.ID
			if n := len(pts) - len(missing); n >= given[key] { // the newer copy is at least as complete
				out[k].Points, out[k].Missing, given[key] = pts, missing, n
			}
			continue
		}
		given[key] = len(pts) - len(missing)
		j := planJSON{ID: m.ID, T: m.T, Callsign: p.Callsign, From: p.From, To: p.To, Points: pts, Missing: missing}
		if a, ok := airports.Lookup(p.From); ok {
			j.FromName = a.Name
		}
		if a, ok := airports.Lookup(p.To); ok {
			j.ToName = a.Name
		}
		seen[key] = len(out)
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}

// dropDetours takes out of a resolved route the named points from the database that send it a long way off and back: the navigation database is from
// 2013, and a fix introduced since can share its name with an older one on the far side of the world. A point is dropped when the
// route goes over 300 nm out of its way to reach it, and is listed as not placed. The points taken out are returned too.
func dropDetours(pts []flightplan.Point, missing []string) (kept []flightplan.Point, notPlaced []string, dropped []flightplan.Point) {
	out := pts[:0:0]
	for i, p := range pts {
		if p.Kind == "fix" {
			if i > 0 && i+1 < len(pts) {
				prev, next := out[len(out)-1], pts[i+1]
				in, onward, direct := geo.DistNM(prev.Lat, prev.Lon, p.Lat, p.Lon), geo.DistNM(p.Lat, p.Lon, next.Lat, next.Lon), geo.DistNM(prev.Lat, prev.Lon, next.Lat, next.Lon)
				if in+onward-direct > 300 && in+onward > 2*direct {
					missing = append(missing, p.Name)
					dropped = append(dropped, p)
					continue
				}
			}
		}
		out = append(out, p)
	}
	return out, missing, dropped
}

func routeKey(p *flightplan.Plan) string {
	var b strings.Builder
	for _, f := range p.Fixes {
		b.WriteString(f.Name)
		b.WriteByte(' ')
	}
	return b.String()
}
