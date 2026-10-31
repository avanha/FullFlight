// Package waypoints puts coordinates to the waypoint names a CPDLC position report carries.
//
// A report names the fix just passed (with when), the next fix (with its ETA) and the one after,
// but never says where they are. A position is taken from the most trustworthy source that has one:
//
//  1. learned: the aircraft's own ADS-C reports list its next waypoint with exact coordinates and an
//     ETA; a CPDLC fix whose time matches that ETA is that waypoint (see Learn).
//  2. database: the navigation database, if its position is consistent with the flight. It is from
//     2013, so a name may be missing or have been reused somewhere else.
//  3. estimated: a passed fix interpolated along the aircraft's own track at the reported time, a next
//     fix projected from the report's position, speed, track and the ETA. The "fix after next" has
//     neither a time nor a way to estimate it, so without 1 or 2 it is not placed.
//
// Only 1 and 2 are marked Exact.
package waypoints

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/avanha/fullflight/internal/geo"
	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/store"
)

const (
	// MatchWindow is how close a CPDLC fix time must be to an ADS-C waypoint ETA to be the same
	// waypoint. CPDLC times are whole minutes, so a minute either side is expected.
	MatchWindow = 90 * time.Second
	// MaxRefDistNM is how far from the aircraft a name's position may be to be believed.
	MaxRefDistNM = 1500.0
	// ConsistentNM is how far a database or learned position may be from our estimate of the same
	// fix before we decide it is a different place that shares the name.
	ConsistentNM = 60.0
	// MaxProjectNM and MaxInterpGap limit the estimates.
	MaxProjectNM = 600.0
	MaxInterpGap = 60 * time.Minute
)

type Waypoint struct {
	Name, Role string
	Lat, Lon   float64
	Exact      bool
	Source     string // "ADS-C", "database", "interpolated" or "projected"
	How        string // plain-language note for a tooltip
	T          time.Time
	AltFt      *float64
}

type Resolver struct {
	Store *store.Store
	Nav   *navdata.DB // may be nil
}

func parseFixes(m store.Message) (store.FixList, bool) {
	var fl store.FixList
	if m.Fixes == "" || json.Unmarshal([]byte(m.Fixes), &fl) != nil {
		return fl, false
	}
	return fl, len(fl.List) > 0
}

// Resolve places the waypoints named in a stored CPDLC position report. Ones it cannot place are
// left out.
func (r *Resolver) Resolve(m store.Message) []Waypoint {
	fl, ok := parseFixes(m)
	if !ok || m.Lat == nil || m.Lon == nil || m.PosT == nil {
		return nil
	}
	ref := [2]float64{*m.Lat, *m.Lon}
	var out []Waypoint
	nextDist := -1.0 // distance from the aircraft to the next fix, once placed
	for _, e := range fl.List {
		w := Waypoint{Name: e.Name, Role: e.Role, AltFt: e.AltFt}
		if e.T != 0 {
			w.T = time.Unix(e.T, 0).UTC()
		}
		est, how, src := r.estimate(m, fl, e, ref)

		switch {
		case r.learned(&w, e.Name, ref, est):
		case r.database(&w, e, ref, fl, est, nextDist):
		case est != nil:
			w.Lat, w.Lon, w.Source, w.How = est[0], est[1], src, how
		default:
			continue
		}
		if e.Role == "next" {
			nextDist = geo.DistNM(ref[0], ref[1], w.Lat, w.Lon)
		}
		out = append(out, w)
	}
	return out
}

// learned uses a position matched from the aircraft's own ADS-C reports. It still must not
// contradict our estimate, in case the name is reused elsewhere.
func (r *Resolver) learned(w *Waypoint, name string, ref [2]float64, est *[2]float64) bool {
	best := nearest(ref, est, func(yield func(lat, lon float64)) {
		for _, c := range r.Store.LearnedFixes(name) {
			yield(c.Lat, c.Lon)
		}
	})
	if best == nil {
		return false
	}
	w.Lat, w.Lon, w.Exact, w.Source = best[0], best[1], true, "ADS-C"
	w.How = "position matched from the aircraft's own ADS-C predicted route"
	return true
}

// database uses the navigation database if a candidate fits the flight: not contradicting our
// estimate if we have one, and for a fix still ahead, actually ahead of the aircraft and no nearer
// than the next fix.
func (r *Resolver) database(w *Waypoint, e store.FixEntry, ref [2]float64, fl store.FixList, est *[2]float64, nextDist float64) bool {
	best := nearest(ref, est, func(yield func(lat, lon float64)) {
		for _, p := range r.Nav.Lookup(e.Name) {
			if e.Role != "passed" && fl.Trk != nil && angleDiff(bearing(ref[0], ref[1], p.Lat, p.Lon), *fl.Trk) > 100 {
				continue // behind the aircraft
			}
			if e.Role == "then" && nextDist >= 0 && geo.DistNM(ref[0], ref[1], p.Lat, p.Lon) < nextDist-10 {
				continue // the fix after next cannot be nearer than the next one
			}
			yield(p.Lat, p.Lon)
		}
	})
	if best == nil {
		return false
	}
	w.Lat, w.Lon, w.Exact, w.Source = best[0], best[1], true, "database"
	w.How = fmt.Sprintf("from the navigation database (cycle %s)", cycle(r.Nav))
	return true
}

func cycle(d *navdata.DB) string {
	if d == nil || d.Cycle == "" {
		return "unknown"
	}
	return d.Cycle
}

// nearest picks, from the candidates a callback yields, the one nearest the aircraft within
// MaxRefDistNM and, if there is an estimate, within ConsistentNM of it.
func nearest(ref [2]float64, est *[2]float64, each func(yield func(lat, lon float64))) *[2]float64 {
	var best *[2]float64
	bestD := MaxRefDistNM
	each(func(lat, lon float64) {
		d := geo.DistNM(ref[0], ref[1], lat, lon)
		if d > bestD {
			return
		}
		if est != nil && geo.DistNM(est[0], est[1], lat, lon) > ConsistentNM {
			return
		}
		best, bestD = &[2]float64{lat, lon}, d
	})
	return best
}

// estimate places a fix from the flight itself: interpolated along the track for one passed, or
// projected from the report for the next. The fix after next has no time, so no estimate.
func (r *Resolver) estimate(m store.Message, fl store.FixList, e store.FixEntry, ref [2]float64) (*[2]float64, string, string) {
	if e.T == 0 {
		return nil, "", ""
	}
	t := time.Unix(e.T, 0).UTC()
	switch e.Role {
	case "passed":
		var before, after *store.Fix
		for _, f := range r.Store.Neighbors(m.Hex, t, MaxInterpGap, 0, 1) {
			f := f
			if !f.T.After(t) && (before == nil || f.T.After(before.T)) {
				before = &f
			}
			if !f.T.Before(t) && (after == nil || f.T.Before(after.T)) {
				after = &f
			}
		}
		if before == nil || after == nil {
			return nil, "", ""
		}
		k := 0.0
		if span := after.T.Sub(before.T); span > 0 {
			k = float64(t.Sub(before.T)) / float64(span)
		}
		return &[2]float64{before.Lat + k*(after.Lat-before.Lat), geo.NormLon(before.Lon + k*geo.DLon(before.Lon, after.Lon))}, "estimated: the aircraft's track at the time it reported passing it", "interpolated"
	case "next":
		if fl.GS == nil || fl.Trk == nil || !t.After(*m.PosT) {
			return nil, "", ""
		}
		dist := *fl.GS * t.Sub(*m.PosT).Hours()
		if dist <= 0 || dist > MaxProjectNM {
			return nil, "", ""
		}
		lat, lon := Advance(ref[0], ref[1], *fl.Trk, dist)
		return &[2]float64{lat, lon}, fmt.Sprintf("estimated: %.0f nm ahead on track %.0f° at %.0f kt, by its ETA", dist, *fl.Trk, *fl.GS), "projected"
	}
	return nil, "", ""
}

// Advance moves dist nautical miles from (lat, lon) along a true track, on a flat approximation
// that is good for the few hundred miles it is used for.
func Advance(lat, lon, trackDeg, distNM float64) (float64, float64) {
	r := trackDeg * math.Pi / 180
	nlat := lat + distNM*math.Cos(r)/60
	nlon := lon + distNM*math.Sin(r)/(60*math.Cos((lat+nlat)/2*math.Pi/180))
	return nlat, geo.NormLon(nlon)
}

func bearing(lat1, lon1, lat2, lon2 float64) float64 {
	dy := lat2 - lat1
	dx := geo.DLon(lon1, lon2) * math.Cos((lat1+lat2)/2*math.Pi/180)
	return math.Mod(math.Atan2(dx, dy)*180/math.Pi+360, 360)
}

func angleDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// routeWaypoint is the part of a stored ADS-C predicted route entry that Learn uses.
type routeWaypoint struct {
	Lat, Lon float64
	ETASec   *float64
	Kind     string // "next", "next+1" or "fixed"; empty in rows stored before it existed
}

type latLon struct{ lat, lon float64 }

// anchor is a waypoint position known exactly, and when the aircraft reached or will reach it.
type anchor struct {
	t         time.Time
	pos       latLon
	source    string
	following []latLon // the waypoints after this one, in order, as the same ADS-C report listed them
}

// routeChain is a stored route's waypoints in order, without the flight computer's fixed-time projection.
func routeChain(wp []routeWaypoint) []routeWaypoint {
	var out []routeWaypoint
	for i, w := range wp {
		if w.Kind == "fixed" || w.Kind == "" && i >= 2 {
			continue
		}
		out = append(out, w)
	}
	return out
}

// wptChangeEvent returns the position an ADS-C waypoint change event reported: the aircraft was at the
// waypoint it had just sequenced. It is only used when it is the position the message was stored with,
// because that is what carries its time.
func wptChangeEvent(m store.Message) (latLon, bool) {
	var d struct {
		Tree struct {
			Arinc622 struct {
				Adsc struct {
					Tags []struct {
						Event *struct{ Lat, Lon *float64 } `json:"wpt_change_event"`
					} `json:"tags"`
				} `json:"adsc"`
			} `json:"arinc622"`
		} `json:"tree"`
	}
	if m.Lat == nil || m.Lon == nil || m.PosT == nil || json.Unmarshal([]byte(m.Decoded), &d) != nil {
		return latLon{}, false
	}
	for _, t := range d.Tree.Arinc622.Adsc.Tags {
		if e := t.Event; e != nil && e.Lat != nil && e.Lon != nil &&
			math.Abs(*e.Lat-*m.Lat) < 1e-4 && math.Abs(*e.Lon-*m.Lon) < 1e-4 {
			return latLon{*e.Lat, *e.Lon}, true
		}
	}
	return latLon{}, false
}

// anchors collects what the aircraft's ADS-C reports say exactly: each predicted waypoint that came
// with an ETA, and each waypoint it reported sequencing.
func anchors(ms []store.Message) (eta, passed []anchor) {
	for _, m := range ms {
		if m.Kind != "adsc" || m.PosT == nil || m.Suspect != "" {
			continue
		}
		var chain []routeWaypoint
		var wp []routeWaypoint
		if m.Route != "" && json.Unmarshal([]byte(m.Route), &wp) == nil {
			chain = routeChain(wp)
		}
		after := func(from int) (out []latLon) {
			for _, w := range chain[from:] {
				out = append(out, latLon{w.Lat, w.Lon})
			}
			return
		}
		for k, w := range chain {
			if w.ETASec != nil {
				eta = append(eta, anchor{m.PosT.Add(time.Duration(*w.ETASec * float64(time.Second))), latLon{w.Lat, w.Lon}, "adsc-eta", after(k + 1)})
			}
		}
		if p, ok := wptChangeEvent(m); ok {
			passed = append(passed, anchor{*m.PosT, p, "adsc-wpt", after(0)})
		}
	}
	return
}

// Learn matches the waypoint names in an aircraft's CPDLC position reports to the exact
// coordinates in its ADS-C reports, by time: a CPDLC fix passed (or due) at a given minute is the
// ADS-C waypoint whose ETA is that minute, or, for a fix passed, the waypoint an ADS-C waypoint
// change event reported sequencing then (a few miles from the waypoint itself, so used only when no
// predicted waypoint has that ETA). The fixes after a matched one in the CPDLC list are the
// waypoints that follow it in that same ADS-C report. It returns how many matches it made; running
// it again over the same messages changes nothing.
func Learn(st *store.Store, hexAddr string, since time.Time) (int, error) {
	ms, err := st.Messages(hexAddr, since, time.Now().Add(48*time.Hour), 100000)
	if err != nil {
		return 0, err
	}
	etas, events := anchors(ms)
	n := 0
	for _, m := range ms {
		fl, ok := parseFixes(m)
		if !ok {
			continue
		}
		reachable := func(p latLon) bool {
			return m.Lat == nil || m.Lon == nil || geo.DistNM(*m.Lat, *m.Lon, p.lat, p.lon) <= MaxRefDistNM
		}
		type learnt struct {
			pos    latLon
			source string
		}
		got := map[int]learnt{}
		chained := map[int]learnt{}
		for i, e := range fl.List {
			if e.T == 0 || e.Role == "then" {
				continue
			}
			t := time.Unix(e.T, 0).UTC()
			var best *anchor
			pools := [][]anchor{etas}
			if e.Role == "passed" {
				// An event puts the aircraft where it sequenced the waypoint, a few miles from it: used
				// only when no predicted waypoint has that ETA.
				pools = append(pools, events)
			}
			for _, pool := range pools {
				for k := range pool {
					d := pool[k].t.Sub(t).Abs()
					if d <= MatchWindow && (best == nil || d < best.t.Sub(t).Abs()) {
						best = &pool[k]
					}
				}
				if best != nil {
					break
				}
			}
			if best == nil || !reachable(best.pos) {
				continue
			}
			got[i] = learnt{best.pos, best.source}
			for j, f := range best.following {
				if i+1+j >= len(fl.List) {
					break
				}
				if reachable(f) {
					chained[i+1+j] = learnt{f, "adsc-chain"}
				}
			}
		}
		for i, l := range chained {
			if _, direct := got[i]; !direct {
				got[i] = l
			}
		}
		for i, l := range got {
			if err := st.AddFix(fl.List[i].Name, m.ID, l.pos.lat, l.pos.lon, l.source); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
