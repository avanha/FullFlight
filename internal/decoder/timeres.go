package decoder

import (
	"math"
	"time"
)

// ClockSkew is how far a receiving station's clock may lag the embedded time
// before we assume the report belongs to the previous hour. We have seen
// stations ~3m20s behind.
const ClockSkew = 5 * time.Minute

// MaxPlausibleKt is the implied ground speed above which two positions from
// the same aircraft are flagged as inconsistent (backfilled uploads etc).
const MaxPlausibleKt = 700.0

// ResolveSecondsPastHour places an ADS-C "seconds past the hour" timestamp:
// the hour that puts the report at or just before recv (allowing ClockSkew).
func ResolveSecondsPastHour(secs float64, recv time.Time) time.Time {
	recv = recv.UTC()
	t := recv.Truncate(time.Hour).Add(time.Duration(secs * float64(time.Second)))
	if t.After(recv.Add(ClockSkew)) {
		t = t.Add(-time.Hour)
	}
	return t
}

type TimedPoint struct {
	Lat, Lon float64
	T        time.Time
}

// ImpliedSpeedKt is the great-circle speed needed to get from a to b.
func ImpliedSpeedKt(a, b TimedPoint) float64 {
	dt := math.Abs(b.T.Sub(a.T).Hours())
	if dt == 0 {
		return math.Inf(1)
	}
	return distNM(a.Lat, a.Lon, b.Lat, b.Lon) / dt
}

func distNM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 3440.065 // earth radius in nautical miles
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// Plausible reports whether the move from a to b is physically reasonable.
func Plausible(a, b TimedPoint) bool { return ImpliedSpeedKt(a, b) <= MaxPlausibleKt }
