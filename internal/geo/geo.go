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
