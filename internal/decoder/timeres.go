package decoder

import (
	"math"
	"time"

	"github.com/avanha/fullflight/internal/geo"
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

// ResolveSecondsOfDay places a time-of-day (seconds since 00:00 UTC) on a date:
// the day that puts it at or just before recv, allowing ClockSkew. CPDLC
// reports carry only hh:mm.
func ResolveSecondsOfDay(secs float64, recv time.Time) time.Time {
	recv = recv.UTC()
	t := recv.Truncate(24 * time.Hour).Add(time.Duration(secs * float64(time.Second)))
	if t.After(recv.Add(ClockSkew)) {
		t = t.Add(-24 * time.Hour)
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
	return geo.DistNM(a.Lat, a.Lon, b.Lat, b.Lon) / dt
}

// Plausible reports whether the move from a to b is physically reasonable.
func Plausible(a, b TimedPoint) bool { return ImpliedSpeedKt(a, b) <= MaxPlausibleKt }
