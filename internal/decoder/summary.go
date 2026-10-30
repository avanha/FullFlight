package decoder

import (
	"fmt"
	"strings"
	"time"
)

// How a Pos time is expressed.
const (
	BasisHour  = "hour"  // seconds past the hour (ADS-C)
	BasisMonth = "month" // seconds since 00:00 UTC on the 1st (REP081)
)

// Pos is a position reported inside a message, with its partial timestamp.
type Pos struct {
	Lat, Lon float64
	AltFt    *float64
	Secs     float64
	Basis    string
}

// At places the position in time, using recv (when the message was received)
// to supply the missing hour or month.
func (p Pos) At(recv time.Time) time.Time {
	if p.Basis == BasisMonth {
		return ResolveMonthSeconds(int64(p.Secs), recv)
	}
	return ResolveSecondsPastHour(p.Secs, recv)
}

// Waypoint is a predicted ADS-C route point; ETASec is relative to the report.
type Waypoint struct {
	Lat, Lon float64
	AltFt    *float64
	ETASec   *float64
}

func num(m map[string]any, k string) *float64 {
	if v, ok := m[k].(float64); ok {
		return &v
	}
	return nil
}

// adscPosition pulls the first position-carrying tag (periodic report,
// waypoint change, ...) and the predicted route out of an ADS-C tree.
func adscPosition(tree map[string]any) (*Pos, []Waypoint) {
	arinc, _ := tree["arinc622"].(map[string]any)
	adsc, _ := arinc["adsc"].(map[string]any)
	tags, _ := adsc["tags"].([]any)
	var pos *Pos
	var route []Waypoint
	for _, t := range tags {
		tm, _ := t.(map[string]any)
		for name, v := range tm {
			body, _ := v.(map[string]any)
			if body == nil {
				continue
			}
			if name == "predicted_route" {
				for _, k := range []string{"next_wpt", "next_next_wpt"} {
					if w, _ := body[k].(map[string]any); w != nil && num(w, "lat") != nil && num(w, "lon") != nil {
						route = append(route, Waypoint{Lat: *num(w, "lat"), Lon: *num(w, "lon"), AltFt: num(w, "alt"), ETASec: num(w, "eta_sec")})
					}
				}
				continue
			}
			if pos == nil && num(body, "lat") != nil && num(body, "lon") != nil && num(body, "ts_sec") != nil {
				pos = &Pos{Lat: *num(body, "lat"), Lon: *num(body, "lon"), AltFt: num(body, "alt"), Secs: *num(body, "ts_sec"), Basis: BasisHour}
			}
		}
	}
	return pos, route
}

func hhmmss(s string) string {
	if len(s) != 6 {
		return s
	}
	return s[:2] + ":" + s[2:4] + ":" + s[4:] + "Z"
}

func latlon(lat, lon float64) string {
	ns, ew := "N", "E"
	if lat < 0 {
		ns, lat = "S", -lat
	}
	if lon < 0 {
		ew, lon = "W", -lon
	}
	return fmt.Sprintf("%.3f%s %.3f%s", lat, ns, lon, ew)
}

func summarize(r Result) string {
	switch r.Kind {
	case KindADSC:
		if r.Pos == nil {
			return "ADS-C message"
		}
		s := "ADS-C report " + latlon(r.Pos.Lat, r.Pos.Lon)
		if r.Pos.AltFt != nil {
			s += fmt.Sprintf(" %.0f ft", *r.Pos.AltFt)
		}
		if len(r.Route) > 0 {
			s += fmt.Sprintf(" → next %s", latlon(r.Route[0].Lat, r.Route[0].Lon))
		}
		return s
	case KindCPDLC:
		if l, ok := findString(r.Tree, "choice_label"); ok {
			return "CPDLC " + map[Direction]string{Uplink: "uplink", Downlink: "downlink"}[r.Dir] + ": " + l
		}
		return "CPDLC message"
	case KindMIAM:
		if rep, ok := r.Fields["rep081"].(REP081Report); ok {
			return fmt.Sprintf("REP081 %s→%s %s FL%03d, OAT %.1f°C, wind %03d°/%dkt",
				rep.Origin, rep.Dest, latlon(rep.Lat, rep.Lon), rep.AltFt/100, rep.TempC, rep.WindDirDeg, rep.WindKt)
		}
		if t, ok := findString(r.Tree, "text"); ok {
			if len(t) > 70 {
				t = t[:70] + "…"
			}
			return "MIAM: " + t
		}
		return "MIAM (not decoded)"
	case KindAFN:
		parts := []string{"AFN"}
		if r.Fields["afn"] == "contact_advisory" {
			parts = []string{fmt.Sprintf("AFN contact advisory: log on to %v", r.Fields["contact_station"])}
		}
		if r.Identity.Callsign != "" {
			parts = append(parts, r.Identity.Callsign)
		}
		return strings.Join(parts, ", ")
	case KindMedia:
		if st, ok := r.Fields["state"].(string); ok {
			return fmt.Sprintf("Link %s: %v at %s (available: %s)", st, r.Fields["link"], hhmmss(fmt.Sprint(r.Fields["time_hhmmss"])),
				strings.Join(r.Fields["available"].([]string), ", "))
		}
		return "Media advisory"
	case KindDATIS:
		return fmt.Sprintf("D-ATIS %v request for %v", r.Fields["atis_type"], r.Fields["airport"])
	}
	return "Unrecognised " + r.Label + " message"
}
