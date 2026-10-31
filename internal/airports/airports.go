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
