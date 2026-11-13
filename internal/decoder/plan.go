package decoder

import (
	"fmt"
	"strings"

	"github.com/avanha/fullflight/internal/airlines"
	"github.com/avanha/fullflight/internal/flightplan"
)

// decodePlan handles the FMS flight plan (FPN/...) and progress (PRG/...) messages, which name the flight.
func decodePlan(m Message) (Result, bool) {
	r := Result{Label: m.Label, Dir: m.Dir, Body: strings.TrimSpace(m.Text), Fields: map[string]any{}}
	if r.Label == "" {
		r.Label = "H1"
	}
	if r.Dir == Unknown {
		r.Dir = Downlink
	}
	if p, ok := flightplan.ParsePlan(m.Text); ok {
		r.Kind = KindPlan
		r.Fields["plan"] = p
		r.Identity = callsignIdentity(p.Callsign)
		who := ""
		if p.Callsign != "" {
			who = p.Callsign + " "
		}
		r.Summary = fmt.Sprintf("Flight plan %s%s → %s, %d fixes", who, p.From, p.To, len(p.Fixes))
		names := make([]string, len(p.Fixes))
		for i, f := range p.Fixes {
			names[i] = f.Name
			if f.Via != "" {
				names[i] += " (" + f.Via + ")"
			}
		}
		r.Text = r.Summary
		if len(names) > 0 {
			r.Text += "\nRoute: " + strings.Join(names, " ")
		}
		return r, true
	}
	if g, ok := flightplan.ParseProgress(m.Text); ok {
		r.Kind = KindProgress
		r.Fields["progress"] = g
		r.Identity = callsignIdentity(g.Callsign)
		s := "Progress report"
		if g.Callsign != "" {
			s += " " + g.Callsign
		}
		if g.To != "" {
			s += " to " + g.To
			if g.Runway != "" {
				s += " runway " + g.Runway
			}
		}
		if len(g.ETA) == 6 {
			s += fmt.Sprintf(", ETA %s:%s:%s", g.ETA[:2], g.ETA[2:4], g.ETA[4:])
			if g.OwnClock {
				// repeats from flight to flight and sits hours from UTC, so it is not a time we can place
				s += " (Aircraft Clock)"
			} else {
				s += "Z"
			}
		}
		r.Summary, r.Text = s, s
		return r, true
	}
	return Result{}, false
}

// callsignIdentity names a flight from its ICAO callsign ("UPS58"), also in the flight-number form ("5X58")
// when the airline is known.
func callsignIdentity(callsign string) Identity {
	if callsign == "" {
		return Identity{}
	}
	_, iata := airlines.Forms(callsign)
	if iata == "" {
		iata = callsign
	}
	return Identity{Callsign: callsign, Flight: iata}
}
