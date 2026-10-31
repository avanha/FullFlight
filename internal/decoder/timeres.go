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

// NearestTimeOfDay places a time of day on the date that puts it closest to ref. An ETA can be
// a little before or after the report that carries it, either side of midnight.
func NearestTimeOfDay(secs float64, ref time.Time) time.Time {
	ref = ref.UTC()
	base := ref.Truncate(24 * time.Hour).Add(time.Duration(secs * float64(time.Second)))
	best := base
	for _, d := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		if c := base.Add(d); c.Sub(ref).Abs() < best.Sub(ref).Abs() {
			best = c
		}
	}
	return best
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

// ResolveMonthDay places a month, day, hour and minute given as the number MMDDHHMM (it has no year) in the year
// that puts it nearest ref, the receive time. Unlike a time of day it can be days, or weeks, before ref: such a
// message was delivered late. A date that does not exist (30 February) is placed at ref.
func ResolveMonthDay(mmddhhmm int, ref time.Time) time.Time {
	ref = ref.UTC()
	month, day, hh, mm := mmddhhmm/1000000, mmddhhmm/10000%100, mmddhhmm/100%100, mmddhhmm%100
	best, ok := time.Time{}, false
	for y := ref.Year() - 1; y <= ref.Year()+1; y++ {
		t := time.Date(y, time.Month(month), day, hh, mm, 0, 0, time.UTC)
		if int(t.Month()) != month || t.Day() != day {
			continue // not a real date that year (29 February)
		}
		if !ok || t.Sub(ref).Abs() < best.Sub(ref).Abs() {
			best, ok = t, true
		}
	}
	if !ok {
		return ref
	}
	return best
}

// ResolveDayTime places a time of day (seconds since 00:00 UTC) that comes with a day of the month, which is the day
// the flight is dated, not always the UTC day of the report (a flight that crosses midnight UTC is still dated
// for its start). It is the latest such time not after recv (allowing ClockSkew) whose UTC day, or the day before it,
// is the one given. That rules out a time of day that falls on an unrelated day, which the time of day alone cannot,
// but a report more than a day late can still be a day out: the 28th and the 29th both fit "dated the 28th" (the
// 29th for a flight that crossed midnight), and the latest is taken. Reports of this kind have been at most about
// twelve hours late. If no day in the last ten matches, the latest time of day before recv is used.
func ResolveDayTime(day int, secs float64, recv time.Time) time.Time {
	recv = recv.UTC()
	base := recv.Truncate(24 * time.Hour)
	var fallback time.Time
	for k := 0; k <= 10; k++ {
		t := base.AddDate(0, 0, -k).Add(time.Duration(secs * float64(time.Second)))
		if t.After(recv.Add(ClockSkew)) {
			continue
		}
		if fallback.IsZero() {
			fallback = t
		}
		if t.Day() == day || t.AddDate(0, 0, -1).Day() == day {
			return t
		}
	}
	if fallback.IsZero() {
		return recv
	}
	return fallback
}
