// Package watchlist models time-windowed watch entries: a flight number plus
// date (watched from before departure) or a tail/hex (watched immediately).
package watchlist

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Kind string

const (
	KindFlight Kind = "flight"
	KindTail   Kind = "tail"
	KindHex    Kind = "hex"
)

type Status string

const (
	StatusWaiting  Status = "waiting"
	StatusTracking Status = "tracking"
	StatusDone     Status = "done"
	StatusExpired  Status = "expired"
)

const (
	FlightLead     = 12 * time.Hour // start watching before the date begins (UTC)
	FlightDuration = 60 * time.Hour // 12 h lead + 1 day of date slop + 36 h of flight
	TailDuration   = 36 * time.Hour // default for tail/hex watches
)

type Entry struct {
	ID       int64
	Kind     Kind
	Ident    string // normalized: "UA869", "B-LRM", "780AB8"
	Date     string // flight entries only, "2006-01-02"
	Start    time.Time
	End      time.Time
	Status   Status
	Hex      string // latched once a flight entry is matched to an aircraft
	Tail     string // registration, once learned
	Callsign string // ICAO callsign, once matched

	// Flight entries: Airborne once the aircraft has been seen flying;
	// LandedAt is set while it sits on the ground after that, and cleared if
	// it takes off again (a stopover).
	Airborne bool
	LandedAt time.Time
}

var (
	flightRe = regexp.MustCompile(`^([A-Z0-9]{2}|[A-Z]{3})(\d{1,4}[A-Z]?)$`)
	hexRe    = regexp.MustCompile(`^[0-9A-F]{6}$`)
	tailRe   = regexp.MustCompile(`^[A-Z0-9]{1,2}-?[A-Z0-9]{2,5}$`)
)

// Parse builds an entry from user input such as "UA869 2026-10-31",
// "B-LRM" or "780AB8". The end of a tail/hex watch is until if non-zero,
// otherwise now + TailDuration. A date on a tail/hex entry is rejected.
func Parse(input string, now, until time.Time) (Entry, error) {
	f := strings.Fields(strings.ToUpper(input))
	switch len(f) {
	case 1:
		return parseAircraft(f[0], now, until)
	case 2:
		return parseFlight(f[0], f[1], now)
	}
	return Entry{}, fmt.Errorf("expected \"UA869 2026-10-31\", a tail, or a hex; got %q", input)
}

func parseFlight(ident, date string, now time.Time) (Entry, error) {
	if !flightRe.MatchString(ident) {
		return Entry{}, fmt.Errorf("%q does not look like a flight number", ident)
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return Entry{}, fmt.Errorf("bad date %q (want YYYY-MM-DD)", date)
	}
	start := day.Add(-FlightLead)
	end := start.Add(FlightDuration)
	if !end.After(now) {
		return Entry{}, fmt.Errorf("window for %s %s already ended", ident, date)
	}
	if start.Before(now) {
		start = now
	}
	return Entry{Kind: KindFlight, Ident: ident, Date: date, Start: start, End: end, Status: StatusWaiting}, nil
}

func parseAircraft(ident string, now, until time.Time) (Entry, error) {
	e := Entry{Ident: ident, Start: now, End: until, Status: StatusTracking}
	if until.IsZero() {
		e.End = now.Add(TailDuration)
	} else if !until.After(now) {
		return Entry{}, fmt.Errorf("end time must be in the future")
	}
	switch {
	case hexRe.MatchString(ident) && !strings.Contains(ident, "-"):
		e.Kind = KindHex
		e.Hex = ident
	case tailRe.MatchString(ident):
		e.Kind = KindTail
	default:
		return Entry{}, fmt.Errorf("%q is not a valid tail or ICAO hex", ident)
	}
	return e, nil
}

// Active reports whether the entry should be polled at t.
func (e Entry) Active(t time.Time) bool {
	return (e.Status == StatusWaiting || e.Status == StatusTracking) &&
		!t.Before(e.Start) && t.Before(e.End)
}

// Expired reports whether an open entry has passed its end.
func (e Entry) Expired(t time.Time) bool {
	return (e.Status == StatusWaiting || e.Status == StatusTracking) && !t.Before(e.End)
}
