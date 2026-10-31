// Package landing infers that an aircraft has landed from where its ADS-B went quiet. Many arrivals
// drop out of coverage on the final approach or the runway, so the last fix is airborne, low and close
// to the airport, and nothing follows. Dead reckoning from such a fix flies the aircraft on along the
// runway heading for as long as it is allowed to, which is wrong: it is parked at the airport.
//
// The inference needs no destination. The last fix must be low, slow and, if its track is known,
// pointing at an airport within a few miles; and the time it would have taken to reach that airport must
// have passed with no newer fix. It is an inference and is shown as one.
package landing

import (
	"fmt"
	"math"
	"time"

	"github.com/avanha/fullflight/internal/airports"
)

const (
	// RadiusNM is how near an airport the last fix must be.
	RadiusNM = 15.0
	// MaxHeightFt is the highest above the field the last fix may be. The altitude is barometric, so
	// this is height above the field to within the pressure setting, a few hundred feet at most.
	MaxHeightFt = 5000.0
	// MaxGS is the fastest the last fix may be going: faster is not an approach.
	MaxGS = 250.0
	// AheadDeg is how far off its track the airport may lie, for a fix with a track.
	AheadDeg = 60.0
	// Margin is how long after the estimated touchdown we wait for a fix before saying it has landed.
	Margin = time.Minute
	// taxiGS is the speed under which an aircraft is taken to be on the ground already.
	taxiGS = 60.0
)

// Fix is the aircraft's newest position.
type Fix struct {
	T                       time.Time
	Lat, Lon                float64
	AltFt, GS, Track, VRate *float64
	OnGround                bool
}

// Landing is the inferred end of a flight.
type Landing struct {
	Airport airports.Airport
	DistNM  float64   // from the last fix to the airport
	At      time.Time // when it probably touched down
	Basis   string    // the evidence, for a tooltip
}

func bearing(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dx := math.Mod(lon2-lon1+540, 360) - 180
	dx *= math.Cos((lat1 + lat2) / 2 * rad)
	return math.Mod(math.Atan2(dx, lat2-lat1)/rad+360, 360)
}

func angleDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// Infer says whether the aircraft whose newest position is f has landed by now. dest is the airport it
// is known to be going to ("" if unknown); it is preferred when it is among those near. The caller
// must pass the newest position the aircraft has from any source, or a later report would be ignored.
func Infer(f Fix, now time.Time, dest string) *Landing {
	hits := airports.Near(f.Lat, f.Lon, RadiusNM)
	if len(hits) == 0 {
		return nil
	}
	// On the ground already: at the nearest airport.
	if f.OnGround {
		h := hits[0]
		return &Landing{h.Airport, h.DistNM, f.T, fmt.Sprintf("last fix on the ground, %.1f nm from %s", h.DistNM, code(h.Airport))}
	}
	if f.AltFt == nil || f.GS == nil || *f.GS > MaxGS {
		return nil
	}
	var best *airports.Hit
	for i := range hits {
		h := &hits[i]
		if height(*f.AltFt, h.Airport) > MaxHeightFt {
			continue // too high above this airport to be landing at it
		}
		if f.Track != nil && angleDiff(bearing(f.Lat, f.Lon, h.Lat, h.Lon), *f.Track) > AheadDeg && h.DistNM > 1 {
			continue // behind it or off to the side: it is not heading there
		}
		if dest != "" && (h.ICAO == dest || h.IATA == dest) {
			best = h
			break
		}
		if best == nil {
			best = h
		}
	}
	if best == nil {
		return nil
	}
	// The time it would have taken to get there.
	eta := time.Duration(0)
	if *f.GS >= taxiGS {
		eta = time.Duration(best.DistNM / *f.GS * float64(time.Hour))
	}
	at := f.T.Add(eta)
	if now.Before(at.Add(Margin)) {
		return nil // it may still be on the way down
	}
	basis := fmt.Sprintf("last fix %.0f ft", *f.AltFt)
	if best.HasElev {
		basis += fmt.Sprintf(" (%.0f above the field)", height(*f.AltFt, best.Airport))
	}
	basis += fmt.Sprintf(", %.0f kt, %.1f nm from %s", *f.GS, best.DistNM, code(best.Airport))
	if f.VRate != nil && *f.VRate < -200 {
		basis += fmt.Sprintf(", descending %.0f ft/min", -*f.VRate)
	}
	return &Landing{best.Airport, best.DistNM, at, basis + ", and nothing since"}
}

// height is the altitude above an airport's elevation, or the altitude itself if the elevation is not known.
func height(altFt float64, a airports.Airport) float64 {
	if a.HasElev {
		return altFt - a.ElevFt
	}
	return altFt
}

func code(a airports.Airport) string {
	if a.ICAO != "" {
		return a.ICAO
	}
	return a.IATA
}
