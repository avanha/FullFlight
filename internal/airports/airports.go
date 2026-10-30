// Package airports looks up airports by IATA or ICAO code. The data is the
// OurAirports dataset (public domain, https://ourairports.com/data/),
// filtered to large and medium airports plus anything with an IATA code.
package airports

import (
	_ "embed"
	"encoding/csv"
	"strconv"
	"strings"
	"sync"
)

//go:embed airports.csv
var data string

type Airport struct {
	ICAO, IATA, Name, City string
	Lat, Lon               float64
}

var (
	once   sync.Once
	byICAO map[string]Airport
	byIATA map[string]Airport
)

func load() {
	byICAO, byIATA = map[string]Airport{}, map[string]Airport{}
	rows, err := csv.NewReader(strings.NewReader(data)).ReadAll()
	if err != nil {
		panic("airports: bad embedded csv: " + err.Error())
	}
	for _, r := range rows[1:] { // header: icao,iata,name,city,lat,lon
		lat, _ := strconv.ParseFloat(r[4], 64)
		lon, _ := strconv.ParseFloat(r[5], 64)
		a := Airport{ICAO: r[0], IATA: r[1], Name: r[2], City: r[3], Lat: lat, Lon: lon}
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
