package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/avanha/fullflight/internal/airports"
)

// Three short messages UPS aircraft exchange around a departure, each naming the route in IATA codes. Read from
// samples; every field below is one the samples agree on.
//
//	label 3J, downlink, a few minutes after takeoff:   "010111 31ANC SDF "
//	      sent ddhhmm (UTC), the day the flight is dated, origin, destination
//	label 3N, uplink, the pre-departure clearance:     "PDC UP0065/31 ANC-SDF\n\t----ATC CLEARANCE----\n ... PROPOSED ETD: 0109Z ..."
//	      airline and number, the day, origin-destination, and the proposed departure time
//	label 41, uplink, the flight's schedule:            "0065 /1031/ANC /SDF /0109/0549/13/10//..."
//	      number, the date (mmdd), origin, destination, then two times (0109 matches the clearance's proposed ETD, so
//	      departure; 0549 is taken to be arrival). The fields after them are not understood.
//
// 3J is the one that proves the leg was flown; the other two are the plan for it.
var (
	ups3J = regexp.MustCompile(`^(\d{2})(\d{4}) (\d{2})([A-Z]{3}) ([A-Z]{3})\s*$`)
	ups3N = regexp.MustCompile(`^PDC ([A-Z0-9]{2})(\d{3,4})/(\d{2}) ([A-Z]{3})-([A-Z]{3})\b`)
	ups41 = regexp.MustCompile(`^(\d{4}) /(\d{2})(\d{2})/([A-Z]{3}) */([A-Z]{3}) */(\d{4})/(\d{4})/`)
	etdRe = regexp.MustCompile(`PROPOSED ETD: (\d{4})Z`)
)

// UPSNotice is one of those messages. It is stored as the message's "departure" field, which is where the route evidence
// looks for an origin and a destination.
type UPSNotice struct {
	What         string // "departure report" (3J), "clearance" (3N) or "schedule" (41)
	Origin, Dest string // ICAO where the airport list knows the IATA code, else as sent
	Airline      string // 3N only: the two-letter code, UP for UPS
	Flight       string // the number without leading zeros
	Day          int    // the day of the month the flight is dated (3J, 3N)
	Date         string // mmdd (41)
	Sent         string // ddhhmm: when it was sent, UTC (3J)
	ETD, ETA     string // hhmm UTC: as planned (3N has the proposed departure only, 41 both)
}

// icaoOf returns the ICAO code of an airport given by its IATA code, or the code itself when the list lacks it.
func icaoOf(iata string) string {
	if a, ok := airports.Lookup(iata); ok && a.ICAO != "" {
		return a.ICAO
	}
	return iata
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ParseUPS reads a UPS 3J, 3N or 41 message; label says which. ok is false if the text is not what the label should carry.
func ParseUPS(label, text string) (UPSNotice, bool) {
	switch label {
	case "3J":
		if m := ups3J.FindStringSubmatch(text); m != nil {
			return UPSNotice{What: "departure report", Sent: m[1] + m[2], Day: atoi(m[3]), Origin: icaoOf(m[4]), Dest: icaoOf(m[5])}, true
		}
	case "3N":
		if m := ups3N.FindStringSubmatch(text); m != nil {
			n := UPSNotice{What: "clearance", Airline: m[1], Flight: strconv.Itoa(atoi(m[2])), Day: atoi(m[3]), Origin: icaoOf(m[4]), Dest: icaoOf(m[5])}
			if e := etdRe.FindStringSubmatch(text); e != nil {
				n.ETD = e[1]
			}
			return n, true
		}
	case "41":
		if m := ups41.FindStringSubmatch(text); m != nil {
			return UPSNotice{What: "schedule", Flight: strconv.Itoa(atoi(m[1])), Date: m[2] + m[3], Origin: icaoOf(m[4]), Dest: icaoOf(m[5]), ETD: m[6], ETA: m[7]}, true
		}
	}
	return UPSNotice{}, false
}

// decodeUPS handles the UPS route messages. It runs before the prefix is looked for, since the start of a clearance
// ("PDC UP0065") and of a schedule ("0065 /1031") has the shape of one.
func decodeUPS(m Message) (Result, bool) {
	n, ok := ParseUPS(m.Label, m.Text)
	if !ok {
		return Result{}, false
	}
	r := Result{Kind: KindDeparture, Label: m.Label, Dir: m.Dir, Body: strings.TrimSpace(m.Text), Fields: map[string]any{"departure": n}}
	if r.Dir == Unknown {
		r.Dir = Uplink
		if m.Label == "3J" {
			r.Dir = Downlink
		}
	}
	if n.Airline != "" { // the clearance names the flight as well as its route
		r.Identity.FlightID = fmt.Sprintf("%s%04s", n.Airline, n.Flight)
		r.Identity.Flight = n.Airline + n.Flight
	}
	r.Summary = n.summary()
	r.Text = r.Summary
	return r, true
}

func hhmmZ(s string) string {
	if len(s) != 4 {
		return s
	}
	return s[:2] + ":" + s[2:] + "Z"
}

func (n UPSNotice) summary() string {
	flight := ""
	if n.Flight != "" {
		flight = " " + n.Airline + n.Flight
	}
	s := fmt.Sprintf("UPS %s%s %s→%s", n.What, flight, n.Origin, n.Dest)
	switch {
	case n.ETD != "" && n.ETA != "":
		s += fmt.Sprintf(", planned %s to %s", hhmmZ(n.ETD), hhmmZ(n.ETA))
	case n.ETD != "":
		s += ", planned departure " + hhmmZ(n.ETD)
	}
	return s
}
