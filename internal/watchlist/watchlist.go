// Package watchlist models time-windowed watch entries: a flight number plus
// date (watched from before departure) or a tail/hex (watched immediately).
package watchlist

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airlines"
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
	// StatusPaused keeps an entry in the list, with everything stored about its aircraft, but nothing is polled
	// for it and it does not expire. Resume it to carry on.
	StatusPaused Status = "paused"
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
	Dest     string // flight entries only, optional: IATA or ICAO code (the server normalises it to ICAO)
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
	destRe   = regexp.MustCompile(`^([A-Z]{3}|[A-Z0-9]{4})$`)
	tailRe   = regexp.MustCompile(`^[A-Z0-9]{1,2}-?[A-Z0-9]{2,5}$`)
)

// Parse builds an entry from user input such as "UA869 2026-10-31",
// "UA869 2026-10-31 SFO" (with destination), "CPA113", "B-LRM" or "780AB8". The end of a tail/hex
// watch is until if non-zero, otherwise now + TailDuration. A date on a tail/hex entry is rejected.
//
// What a lone word is is worked out from its shape (see isBareFlight). Where that cannot be told,
// say so with a prefix: "flight:CK624", "tail:CKK624" or "hex:ABC123".
func Parse(input string, now, until time.Time) (Entry, error) {
	f := strings.Fields(strings.ToUpper(input))
	force := Kind("")
	if len(f) > 0 {
		k, rest, err := splitPrefix(f[0])
		if err != nil {
			return Entry{}, err
		}
		force, f[0] = k, rest
	}
	if (force == KindTail || force == KindHex) && len(f) != 1 {
		return Entry{}, fmt.Errorf("a %s watch takes just the %s, no date or destination", force, force)
	}
	bare := force == KindFlight || (force == "" && len(f) > 0 && isBareFlight(f[0]))
	switch len(f) {
	case 1:
		if bare {
			return parseBareFlight(f[0], "", now, until)
		}
		return parseAircraft(f[0], force, now, until)
	case 2:
		// "CPA113 SFO": a callsign with a destination and no date.
		if bare && destRe.MatchString(f[1]) && !dateRe.MatchString(f[1]) {
			return parseBareFlight(f[0], f[1], now, until)
		}
		fallthrough
	case 3:
		e, err := parseFlight(f[0], f[1], now)
		if err == nil && len(f) == 3 {
			if !destRe.MatchString(f[2]) {
				return Entry{}, fmt.Errorf("%q is not an airport code (3-letter IATA or 4-character ICAO)", f[2])
			}
			e.Dest = f[2]
		}
		return e, err
	}
	return Entry{}, fmt.Errorf("expected \"UA869 2026-10-31 [SFO]\", a callsign, a tail, or a hex; got %q", input)
}

var prefixKinds = map[string]Kind{
	"FLIGHT": KindFlight, "CALLSIGN": KindFlight, "FLT": KindFlight,
	"TAIL": KindTail, "REG": KindTail, "REGISTRATION": KindTail,
	"HEX": KindHex, "ICAO": KindHex,
}

// splitPrefix reads an optional "flight:", "tail:" or "hex:" off the front of a word.
func splitPrefix(w string) (Kind, string, error) {
	name, rest, ok := strings.Cut(w, ":")
	if !ok {
		return "", w, nil
	}
	k, known := prefixKinds[name]
	if !known {
		return "", "", fmt.Errorf("unknown prefix %q (use flight:, tail: or hex:)", name+":")
	}
	if rest == "" {
		return "", "", fmt.Errorf("nothing after %q", name+":")
	}
	return k, rest, nil
}

var (
	icaoCallsignRe = regexp.MustCompile(`^([A-Z]{3})\d{1,4}[A-Z]?$`)
	iataFlightRe   = regexp.MustCompile(`^([A-Z0-9]{2})\d{1,4}[A-Z]?$`)
	dateRe         = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// isBareFlight reports whether a lone word is a flight rather than an aircraft.
//
// An ICAO callsign is three letters then digits (CPA113, TZP1, CKK624), whatever the airline: no
// registration looks like that, since they are hyphenated (B-LRM) or start N, JA, HL and so on. The one
// clash is a six-character word made only of hex digits (AEE123), which is also an ICAO address; that
// is a callsign only when we know the airline, otherwise an address. A two-letter flight number
// (UA869) is a flight only for an airline code we know, since JA123A is a registration; for another
// airline, write flight:CK624.
func isBareFlight(s string) bool {
	if m := icaoCallsignRe.FindStringSubmatch(s); m != nil {
		return !hexRe.MatchString(s) || airlines.IsICAO(m[1])
	}
	m := iataFlightRe.FindStringSubmatch(s)
	return m != nil && airlines.IsIATA(m[1])
}

// parseBareFlight watches a flight with no date: from now until until, or for
// TailDuration by default (long enough for the longest flights).
func parseBareFlight(ident, dest string, now, until time.Time) (Entry, error) {
	e := Entry{Kind: KindFlight, Ident: ident, Start: now, End: until, Status: StatusWaiting}
	if until.IsZero() {
		e.End = now.Add(TailDuration)
	} else if !until.After(now) {
		return Entry{}, fmt.Errorf("end time must be in the future")
	}
	if dest != "" {
		if !destRe.MatchString(dest) {
			return Entry{}, fmt.Errorf("%q is not an airport code (3-letter IATA or 4-character ICAO)", dest)
		}
		e.Dest = dest
	}
	return e, nil
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

// parseAircraft reads a tail or an ICAO hex. force picks one when the caller said which; otherwise it
// is a hex if it is six hex digits, else a tail.
func parseAircraft(ident string, force Kind, now, until time.Time) (Entry, error) {
	e := Entry{Ident: ident, Start: now, End: until, Status: StatusTracking}
	if until.IsZero() {
		e.End = now.Add(TailDuration)
	} else if !until.After(now) {
		return Entry{}, fmt.Errorf("end time must be in the future")
	}
	isHex := hexRe.MatchString(ident) && !strings.Contains(ident, "-")
	switch {
	case force == KindHex && !isHex:
		return Entry{}, fmt.Errorf("%q is not an ICAO hex address (six hex digits)", ident)
	case force == KindHex || (force == "" && isHex):
		e.Kind = KindHex
		e.Hex = ident
	case force == KindTail || tailRe.MatchString(ident):
		if !tailRe.MatchString(ident) {
			return Entry{}, fmt.Errorf("%q does not look like a registration", ident)
		}
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

// Pause stops an open entry being polled. It stays in the list, and its data is kept, until it is resumed or removed.
func (e *Entry) Pause() error {
	if e.Status != StatusWaiting && e.Status != StatusTracking {
		return fmt.Errorf("only a watch that is waiting or tracking can be paused, not one that is %s", e.Status)
	}
	e.Status = StatusPaused
	return nil
}

// Resume restarts a paused entry: tracking if it is matched to an aircraft, else waiting. A tail or hex watch
// gets the usual TailDuration from now if it would end sooner (it was paused, so it never ran down); a flight
// watch keeps its window, and cannot be resumed once that has passed.
func (e *Entry) Resume(now time.Time) error {
	if e.Status != StatusPaused {
		return fmt.Errorf("only a paused watch can be resumed, not one that is %s", e.Status)
	}
	if e.Kind == KindFlight {
		if !now.Before(e.End) {
			return fmt.Errorf("the window for this flight has ended: watch it again")
		}
	} else if end := now.Add(TailDuration); e.End.Before(end) {
		e.End = end
	}
	e.Status = StatusWaiting
	if e.Hex != "" {
		e.Status = StatusTracking
	}
	return nil
}

// Expired reports whether an open entry has passed its end.
func (e Entry) Expired(t time.Time) bool {
	return (e.Status == StatusWaiting || e.Status == StatusTracking) && !t.Before(e.End)
}
