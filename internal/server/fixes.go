package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/geo"
)

// maxDatabaseFixes bounds how many navigation database points one search returns.
const maxDatabaseFixes = 60

// sameFixNM is how close a database point of the same name must be to a learned one to be the same fix.
const sameFixNM = 5.0

type fixJSON struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	// Kind is "learned" (matched from the aircraft's own messages) or the database's: fix, vor, ndb, dme.
	Kind    string     `json:"kind"`
	N       int        `json:"n,omitempty"`     // learned: messages that support the position
	First   *time.Time `json:"first,omitempty"` // learned: when the earliest of them was received
	Last    *time.Time `json:"last,omitempty"`  // learned: and the latest
	Sources []string   `json:"sources,omitempty"`
	// DBNM is the distance to the database's point of the same name, when it has one (learned only).
	DBNM *float64 `json:"db_nm,omitempty"`
}

type fixesJSON struct {
	LearnedNames     int       `json:"learned_names"`
	LearnedPositions int       `json:"learned_positions"`
	DatabaseNames    int       `json:"database_names"`
	DatabaseCycle    string    `json:"database_cycle,omitempty"`
	More             bool      `json:"more,omitempty"` // the database had more matches than are listed
	Fixes            []fixJSON `json:"fixes"`
}

// fixes serves the fix dataset: every position learned from messages, or those whose name contains q,
// with the navigation database's points for names starting with q (two letters at least).
func (s *Server) fixes(w http.ResponseWriter, r *http.Request) {
	q := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("q")))
	sums, err := s.Store.FixSummaries()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := fixesJSON{LearnedPositions: len(sums), DatabaseNames: s.Nav.Len(), Fixes: []fixJSON{}}
	if s.Nav != nil {
		out.DatabaseCycle = s.Nav.Cycle
	}
	names := map[string]bool{}
	for _, f := range sums {
		names[f.Name] = true
		if q != "" && !strings.Contains(f.Name, q) {
			continue
		}
		j := fixJSON{Name: f.Name, Lat: f.Lat, Lon: f.Lon, Kind: "learned", N: f.N, Sources: f.Sources}
		if !f.First.IsZero() {
			first, last := f.First, f.Last
			j.First, j.Last = &first, &last
		}
		for _, p := range s.Nav.Lookup(f.Name) {
			if d := geo.DistNM(f.Lat, f.Lon, p.Lat, p.Lon); j.DBNM == nil || d < *j.DBNM {
				d := d
				j.DBNM = &d
			}
		}
		out.Fixes = append(out.Fixes, j)
	}
	out.LearnedNames = len(names)
	sort.SliceStable(out.Fixes, func(i, j int) bool { return out.Fixes[i].Name < out.Fixes[j].Name })

	if len(q) >= 2 {
		ms, more := s.Nav.Search(q, maxDatabaseFixes)
		out.More = more
	next:
		for _, m := range ms {
			for _, l := range out.Fixes { // a learned position that is this point: shown once
				if l.Kind == "learned" && l.Name == m.Name && geo.DistNM(l.Lat, l.Lon, m.Lat, m.Lon) <= sameFixNM {
					continue next
				}
			}
			out.Fixes = append(out.Fixes, fixJSON{Name: m.Name, Lat: m.Lat, Lon: m.Lon, Kind: m.Kind})
		}
	}
	writeJSON(w, 200, out)
}
