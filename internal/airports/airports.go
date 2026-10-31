// Package airports looks up airports by IATA or ICAO code. The data is the
// OurAirports dataset (public domain, https://ourairports.com/data/),
// filtered to large and medium airports plus anything with an IATA code.
package airports

import (
	_ "embed"
	"encoding/csv"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/avanha/fullflight/internal/geo"
)

//go:embed airports.csv
var data string

type Airport struct {
	ICAO, IATA, Name, City string
	Lat, Lon               float64
	ElevFt                 float64 // field elevation above mean sea level
	HasElev                bool    // false if the data has none
}

var (
	once   sync.Once
	byICAO map[string]Airport
	byIATA map[string]Airport
	all    []Airport
)

func load() {
	byICAO, byIATA = map[string]Airport{}, map[string]Airport{}
	rows, err := csv.NewReader(strings.NewReader(data)).ReadAll()
	if err != nil {
		panic("airports: bad embedded csv: " + err.Error())
	}
	for _, r := range rows[1:] { // header: icao,iata,name,city,lat,lon,elevation_ft
		lat, _ := strconv.ParseFloat(r[4], 64)
		lon, _ := strconv.ParseFloat(r[5], 64)
		a := Airport{ICAO: r[0], IATA: r[1], Name: r[2], City: r[3], Lat: lat, Lon: lon}
		if len(r) > 6 && r[6] != "" {
			if e, err := strconv.ParseFloat(r[6], 64); err == nil {
				a.ElevFt, a.HasElev = e, true
			}
		}
		all = append(all, a)
		byICAO[a.ICAO] = a
		if a.IATA != "" {
			if _, dup := byIATA[a.IATA]; !dup {
				byIATA[a.IATA] = a
			}
		}
	}
}

// Lookup finds an airport by a 3-letter IATA or 4-character ICAO code.
func Lookup(code string) (Airport, bool) {
	once.Do(load)
	code = strings.ToUpper(strings.TrimSpace(code))
	var a Airport
	var ok bool
	switch len(code) {
	case 3:
		a, ok = byIATA[code]
	case 4:
		a, ok = byICAO[code]
	}
	return a, ok
}

// Hit is an airport and how far it is from a point.
type Hit struct {
	Airport
	DistNM float64
}

// Near returns the airports within maxNM of a position, nearest first.
func Near(lat, lon, maxNM float64) []Hit {
	once.Do(load)
	var out []Hit
	for _, a := range all {
		if d := geo.DistNM(lat, lon, a.Lat, a.Lon); d <= maxNM {
			out = append(out, Hit{a, d})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DistNM < out[j].DistNM })
	return out
}

// Search finds airports whose ICAO or IATA code, name or city matches q (case-insensitive), best match
// first: an exact code, then a code that starts with q, then a name or city that starts with it or has a word
// that does, then any containing it. At most limit are returned; more says whether there were others.
func Search(q string, limit int) (out []Airport, more bool) {
	once.Do(load)
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil, false
	}
	type scored struct {
		Airport
		score int
	}
	var hits []scored
	for _, a := range all {
		s := 99
		icao, iata := strings.ToLower(a.ICAO), strings.ToLower(a.IATA)
		switch {
		case icao == q || iata == q:
			s = 0
		case strings.HasPrefix(icao, q) || (iata != "" && strings.HasPrefix(iata, q)):
			s = 1
		default:
			for _, f := range []string{strings.ToLower(a.Name), strings.ToLower(a.City)} {
				if strings.HasPrefix(f, q) || strings.Contains(f, " "+q) {
					s = min(s, 2)
				} else if strings.Contains(f, q) {
					s = min(s, 3)
				}
			}
		}
		if s < 99 {
			hits = append(hits, scored{a, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return hits[i].ICAO < hits[j].ICAO
	})
	for i, h := range hits {
		if i == limit {
			return out, true
		}
		out = append(out, h.Airport)
	}
	return out, false
}

// Count is how many airports the data holds.
func Count() int {
	once.Do(load)
	return len(all)
}
