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
	Kind     string             `json:"kind,omitempty"`    // "clearance": an oceanic clearance, the route over the ocean only, not a flight plan
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
		if m.Kind != "flight_plan" {
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

func routeKey(p *flightplan.Plan) string {
	var b strings.Builder
	for _, f := range p.Fixes {
		b.WriteString(f.Name)
		b.WriteByte(' ')
	}
	return b.String()
}
