package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The departure report some Delta aircraft send (label 4M, or inside a MIAM message) as they leave the gate, about 20
// seconds after they start to taxi:
//
//	28064M KLAX VHHH7
//	/FN 0087
//	TAXI/HEADING 083
//	    /BRAKE SET  064813/BRAKE RLSD 065213
//	    /MOVEMENT   065301/ENGINE RUN 064847
//	OUT /HEADING 223
//	    /OUT EVENT  064443/MOVE EVENT 064548
//	    /BRAKE RLSD 064442/CABIN CLSD 063634
//	    /GALLY CLSD 045628/CARGO CLSD 063720
//
// Origin, destination and flight number are plain. Every six digit number is a time of day (hhmmss UTC, "------" when
// the event did not happen) and the headings are degrees. Read from samples: what the leading "28064M" and the digit
// after the destination mean is not known (the 28 matches the local date at the origin).
var (
	departureHead = regexp.MustCompile(`^(\d{2})(\d{2})(\w{2}) ([A-Z]{4}) ([A-Z]{4})(\d?)[ \t]*\r?\n/FN (\d{1,4})\b`)
	departureTok  = regexp.MustCompile(`(TAXI|OUT) */HEADING (\d{3})|(BRAKE SET|BRAKE RLSD|MOVEMENT|ENGINE RUN|OUT EVENT|MOVE EVENT|CABIN CLSD|GALLY CLSD|CARGO CLSD) +(\d{6}|-{6})`)
)

// DepartureReport is a gate departure report. The "taxi" section holds the events from the aircraft starting to move
// and the "out" section those of the push back before it.
type DepartureReport struct {
	Origin, Dest string
	Flight       string                       // the number, no airline: the report does not give one
	Heading      map[string]int               // "taxi" and "out"
	Events       map[string]map[string]string // section -> event ("BRAKE SET") -> hhmmss
}

// ParseDeparture reads a departure report. A report cut short (the first block of a long one) gives what is there.
func ParseDeparture(text string) (DepartureReport, bool) {
	m := departureHead.FindStringSubmatch(text)
	if m == nil {
		return DepartureReport{}, false
	}
	n, _ := strconv.Atoi(m[7])
	rep := DepartureReport{Origin: m[4], Dest: m[5], Flight: strconv.Itoa(n), Heading: map[string]int{}, Events: map[string]map[string]string{}}
	section := ""
	for _, t := range departureTok.FindAllStringSubmatch(text, -1) {
		if t[1] != "" {
			section = strings.ToLower(strings.TrimSpace(t[1]))
			rep.Heading[section], _ = strconv.Atoi(t[2])
			continue
		}
		if section == "" || strings.Trim(t[4], "-") == "" {
			continue
		}
		if rep.Events[section] == nil {
			rep.Events[section] = map[string]string{}
		}
		rep.Events[section][t[3]] = t[4]
	}
	return rep, true
}

func (r DepartureReport) summary() string {
	s := fmt.Sprintf("Departure report flight %s %s→%s", r.Flight, r.Origin, r.Dest)
	var ev []string
	if t := r.Events["out"]["OUT EVENT"]; t != "" {
		ev = append(ev, "out "+hhmmss(t))
	}
	if t := r.Events["taxi"]["MOVEMENT"]; t != "" {
		ev = append(ev, "taxi "+hhmmss(t))
	}
	if len(ev) > 0 {
		s += ": " + strings.Join(ev, ", ")
	}
	return s
}
