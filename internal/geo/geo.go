// Package geo holds small geodesy helpers.
package geo

import "math"

const earthRadiusNM = 3440.065

// DistNM is the great-circle distance in nautical miles.
func DistNM(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusNM * math.Asin(math.Sqrt(h))
}

// NormLon wraps a longitude into [-180, 180).
func NormLon(lon float64) float64 {
	return math.Mod(math.Mod(lon+180, 360)+360, 360) - 180
}

// DLon is the signed shortest difference b - a in longitude, in (-180, 180]: 2 for 179 to -179, not -358.
func DLon(a, b float64) float64 {
	d := math.Mod(b-a, 360)
	switch {
	case d > 180:
		d -= 360
	case d <= -180:
		d += 360
	}
	return d
}
