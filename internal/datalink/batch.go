package datalink

import (
	"fmt"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// BatchWindow is how near in arrival time, at the same station, a message must be to one that was delivered late
// to be taken as part of the same backlog.
const BatchWindow = 30 * time.Minute

// lateBatchPrefix starts the TimeNote MarkLate writes, so it can tell its own verdicts from the others.
const lateBatchPrefix = "in a late batch: "

// datedLate describes a message that carries its own full date and time against when it arrived. Delivered
// late (over TimeTolerance), it is placed at its own time, which is right, so it is not unverified: the note
// only says it is old. Dated in the future, its clock is wrong and its time cannot be trusted.
func datedLate(arrived, own time.Time) (note string, flag bool) {
	d := arrived.Sub(own)
	switch {
	case d > TimeTolerance:
		return fmt.Sprintf("delivered %s after its own time, %s: it is placed at that time", humanDur(d), own.UTC().Format("2006-01-02 15:04Z")), false
	case d < -TimeTolerance:
		return fmt.Sprintf("dated %s after it arrived: its own clock is wrong, so its time is unverified", humanDur(-d)), true
	}
	return "", false
}

func humanDur(d time.Duration) string {
	d = d.Round(time.Minute)
	if h := int(d.Hours()); h >= 48 {
		return fmt.Sprintf("%dh %dm (%.1f days)", h, int(d.Minutes())%60, d.Hours()/24)
	} else if h > 0 {
		return fmt.Sprintf("%dh %dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// ownTime returns the true time of a message that carries a full date and time of its own that has been checked
// against messages sent on time: a *POS report. Others carry dates too (a progress report's TS field, an FMS
// waypoint time) but those are not UTC in the samples we have, so they say nothing about lateness and are not
// used as clocks.
func ownTime(m store.Message) (time.Time, bool) {
	if m.Kind == "pos_report" && m.PosT != nil {
		return *m.PosT, true
	}
	return time.Time{}, false
}

// MarkLate finds messages that arrived with a backlog a station delivered late, and flags them as time-unverified.
// Some messages carry a full date (see ownTime), so their lateness is known. Others (an ADS-C report holds only
// minutes and seconds past the hour, a CPDLC or AFN message a time of day) cannot say: the app dates them by
// arrival, which for a message from three days ago puts the aircraft where it was then, as if now. So a message
// that arrived at the same station within BatchWindow of a dated one, which was late, nearest to it, is taken to
// be as late and flagged; one nearest to a dated message that was on time is left alone. Messages from other
// stations are not judged by it: a backlog from one station can arrive among live messages from others.
//
// It returns how many messages it changed, and running it again over the same messages changes nothing.
func MarkLate(st *store.Store, hexAddr string, since time.Time) (int, error) {
	ms, err := st.Messages(hexAddr, since.Add(-BatchWindow), time.Now().Add(48*time.Hour), 100000)
	if err != nil {
		return 0, err
	}
	type clock struct {
		arrived, own time.Time
		station      string
	}
	var clocks []clock
	for _, m := range ms {
		if own, ok := ownTime(m); ok && m.Station != "" {
			clocks = append(clocks, clock{m.T, own, m.Station})
		}
	}
	changed := 0
	for _, m := range ms {
		if _, dated := ownTime(m); dated || m.Kind == "fms_position" || m.Station == "" || m.T.Before(since) {
			continue // those carry their own verdicts
		}
		if m.TimeNote != "" && !strings.HasPrefix(m.TimeNote, lateBatchPrefix) {
			continue
		}
		var near *clock
		for i := range clocks {
			c := &clocks[i]
			if c.station != m.Station || m.T.Sub(c.arrived).Abs() > BatchWindow {
				continue
			}
			if near == nil || m.T.Sub(c.arrived).Abs() < m.T.Sub(near.arrived).Abs() {
				near = c
			}
		}
		note, flag := "", false
		if near != nil {
			if late := near.arrived.Sub(near.own); late > TimeTolerance {
				note = lateBatchPrefix + fmt.Sprintf("%s delivered a message at %s that was dated %s (%s earlier), so this one is probably old too: its time is unverified",
					m.Station, near.arrived.UTC().Format("15:04:05Z"), near.own.UTC().Format("2006-01-02 15:04Z"), humanDur(late))
				flag = true
			}
		}
		if note != m.TimeNote || flag != m.TimeFlag {
			m.TimeNote, m.TimeFlag = note, flag
			if err := st.UpdateDecoded(m); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}
