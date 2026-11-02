package store

import (
	"encoding/json"
	"strconv"
	"time"
)

// The time a message counts as, for the flight it belongs to, is worked out once, when it is stored (or decoded again), and kept in
// the message row: At and AtBasis. Everything that places a message in a flight reads that column and none of it repeats the rules.
//
// A message normally counts from when it arrived. Some say when they were sent, and a station may deliver one hours or days late:
// then it belongs to the flight it was sent in, so the time the aircraft put in it counts, where that is a full date and time (or
// a day of the month and a time of day) and not just a time of day we would have to place by guessing.
const (
	AtReceipt   = "receipt"   // no time of its own: it counts from when it arrived
	AtEvent     = "event"     // the time the message gives for itself
	AtScheduled = "scheduled" // about a flight that has not left yet: it counts from when that flight is due to leave
)

// eventFields is what of a message's decoded JSON decides the time.
type eventFields struct {
	Fields struct {
		Ops *struct {
			At    string            `json:"at"`
			Sched string            `json:"sched"`
			Info  map[string]string `json:"info"`
		} `json:"ops"`
		A350    *struct{ Time *string }     `json:"a350"`
		RTR     *struct{ Time *string }     `json:"rtr"`
		Takeoff *struct{ Time *string }     `json:"takeoff"`
		Rep081  json.RawMessage             `json:"rep081"`
		PDC     *struct{ Day, Time string } `json:"pdc"`
	} `json:"fields"`
}

// positionTimed lists the kinds of report whose position time is the time of the report, because they give the date.
var positionTimed = map[string]bool{"acmf_report": true, "aoc_position": true, "pos_report": true, "posrpt": true}

// EventTime says when a message counts as, and on what basis. rx is when it arrived, posT the time of the position it
// carries, if any, and decoded its decoded JSON.
//
// In order, the first that applies: the full time of an operations event; the time in an A350 report (a report with the zero time
// has none); the position time of a REP081 or of the kinds that give a date (ACMF, AOC, *POS, POSRPT); an operations event's or a
// pre-departure clearance's day of the month and time of day. Otherwise the arrival, or, for a flight not yet left, when it is due.
func EventTime(rx time.Time, kind string, posT *time.Time, decoded string) (time.Time, string) {
	var f eventFields
	if decoded != "" {
		json.Unmarshal([]byte(decoded), &f) // not JSON, or not this shape: no time of its own
	}
	fd := f.Fields
	if fd.Ops != nil {
		if t, ok := parseTime(fd.Ops.At); ok {
			return t, AtEvent
		}
	}
	for _, x := range []*struct{ Time *string }{fd.A350, fd.RTR, fd.Takeoff} {
		if x == nil || x.Time == nil {
			continue
		}
		// the first of the three that has a time field is the one, even if it turns out to be the zero time
		if t, ok := parseTime(*x.Time); ok && t.Unix() > a350Earliest {
			return t, AtEvent
		}
		break
	}
	if posT != nil {
		if kind == "miam" && len(fd.Rep081) > 0 && string(fd.Rep081) != "null" {
			return *posT, AtEvent
		}
		if positionTimed[kind] {
			return *posT, AtEvent
		}
	}
	if fd.Ops != nil {
		if t, ok := dayTime(rx, fd.Ops.Info["day"], fd.Ops.Info["time"]); ok {
			return t, AtEvent
		}
	}
	if fd.PDC != nil {
		if t, ok := dayTime(rx, fd.PDC.Day, fd.PDC.Time); ok {
			return t, AtEvent
		}
	}
	if fd.Ops != nil {
		if t, ok := parseTime(fd.Ops.Sched); ok && t.After(rx) {
			return t, AtScheduled
		}
	}
	return rx, AtReceipt
}

// a350Earliest (1 January 2000) is the earliest time an A350 report can truly give; a report with no time has the zero time.
const a350Earliest = 946684800

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// dayTime places a day of the month ("27") and a time of day ("11:58Z") that was sent in a message received at rx: that day of the
// month in the month it arrived, or the month before if that would be after it arrived. Such messages are often delivered late.
func dayTime(rx time.Time, day, tod string) (time.Time, bool) {
	if len(tod) < 5 || tod[2] != ':' || day == "" || day[0] < '0' || day[0] > '9' {
		return time.Time{}, false
	}
	d := leadingInt(day)
	if d < 1 || d > 31 {
		return time.Time{}, false
	}
	hh, mm := leadingInt(tod[:2]), leadingInt(tod[3:5])
	rx = rx.UTC()
	in := func(month time.Month) time.Time {
		return time.Date(rx.Year(), month, d, hh, mm, 0, 0, time.UTC)
	}
	if this := in(rx.Month()); !this.After(rx.Add(time.Hour)) {
		return this, true
	}
	return in(rx.Month() - 1), true
}

// leadingInt reads the digits a string starts with, 0 if there are none.
func leadingInt(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	v, _ := strconv.Atoi(s[:n])
	return v
}
