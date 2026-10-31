package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/avanha/fullflight/internal/airports"
)

// maxAirports bounds how many airports one search returns.
const maxAirports = 50

type airportJSON struct {
	ICAO   string   `json:"icao"`
	IATA   string   `json:"iata,omitempty"`
	Name   string   `json:"name"`
	City   string   `json:"city,omitempty"`
	Lat    float64  `json:"lat"`
	Lon    float64  `json:"lon"`
	ElevFt *float64 `json:"elev_ft,omitempty"`
	// Why says what in your watches uses this airport: "UA869 destination", "N24993 route origin", "JA826J landed".
	Why []string `json:"why,omitempty"`
}

type airportsJSON struct {
	Total    int           `json:"total"`             // airports in the data
	Watched  bool          `json:"watched,omitempty"` // no query: these are the ones your watches use
	More     bool          `json:"more,omitempty"`
	Airports []airportJSON `json:"airports"`
}

func toAirportJSON(a airports.Airport, why []string) airportJSON {
	j := airportJSON{ICAO: a.ICAO, IATA: a.IATA, Name: a.Name, City: a.City, Lat: a.Lat, Lon: a.Lon, Why: why}
	if a.HasElev {
		e := a.ElevFt
		j.ElevFt = &e
	}
	return j
}

// watchAirports lists, by ICAO code, what in the watches uses each airport: a destination, either end of the
// route the aircraft reported, or a landing we inferred.
func (s *Server) watchAirports() (map[string][]string, error) {
	es, err := s.Store.ListWatches(false)
	if err != nil {
		return nil, err
	}
	why := map[string][]string{}
	add := func(code, what string) {
		a, ok := airports.Lookup(code)
		if !ok || code == "" {
			return
		}
		for _, w := range why[a.ICAO] {
			if w == what {
				return
			}
		}
		why[a.ICAO] = append(why[a.ICAO], what)
	}
	for _, e := range es {
		j := s.withAircraft(toJSON(e))
		add(j.Dest, fmt.Sprintf("%s destination", j.Ident))
		if r := j.Route; r != nil {
			add(r.From, fmt.Sprintf("%s route origin (%s)", j.Ident, r.Source))
			add(r.To, fmt.Sprintf("%s route destination (%s)", j.Ident, r.Source))
		}
		if j.Landing != nil {
			add(j.Landing.Code, fmt.Sprintf("%s landed", j.Ident))
		}
	}
	return why, nil
}

// airports serves the airport data: with ?q=, the airports matching it by code, name or city; without, the
// airports the watches use.
func (s *Server) airports(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	why, err := s.watchAirports()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := airportsJSON{Total: airports.Count(), Airports: []airportJSON{}}
	if q == "" {
		out.Watched = true
		for code, reasons := range why {
			if a, ok := airports.Lookup(code); ok {
				out.Airports = append(out.Airports, toAirportJSON(a, reasons))
			}
		}
		sortAirports(out.Airports)
		writeJSON(w, 200, out)
		return
	}
	found, more := airports.Search(q, maxAirports)
	out.More = more
	for _, a := range found {
		out.Airports = append(out.Airports, toAirportJSON(a, why[a.ICAO]))
	}
	writeJSON(w, 200, out)
}

func sortAirports(a []airportJSON) {
	sort.Slice(a, func(i, j int) bool { return a[i].ICAO < a[j].ICAO })
}
