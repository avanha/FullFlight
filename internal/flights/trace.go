package flights

import (
	"fmt"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// Note is one thing the flight logic did, and why: a sighting set aside, two pieces joined (or kept apart), a flight cut at a
// stop, a route or an origin chosen. It concerns the span From to To, and Explain attaches it to the flights that span touches.
type Note struct {
	Stage  string    `json:"stage"` // where in the work it happened: evidence, runs, positions, stops, route, departure, manual
	Rule   string    `json:"rule"`  // which rule: stale, weak, stray, bridge, run, dropped, unidentified, split, unseen-stop, route, join, not-joined, seen-origin, manual-route, departure, manual-join
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Detail string    `json:"detail"`
}

// trace collects the notes of one run of the flight logic. A nil trace records nothing, so the usual run pays nothing for it.
type trace struct{ notes []Note }

func (tr *trace) note(stage, rule string, from, to time.Time, format string, args ...any) {
	if tr == nil {
		return
	}
	tr.notes = append(tr.notes, Note{Stage: stage, Rule: rule, From: from, To: to, Detail: fmt.Sprintf(format, args...)})
}

// slack is how far outside a flight a note may fall and still be about it. Most notes are about what happened inside the flight;
// those about a gap between flights (a stop on the ground, two pieces kept apart) are about both ends of it, and a sighting set
// aside is just before or after the flight it was set aside from.
func (n Note) slack() time.Duration {
	switch n.Rule {
	case "split", "unseen-stop", "not-joined":
		return AttachSlack
	case "stale", "weak":
		return 10 * time.Minute
	}
	return 0
}

// Explanation is the flights in a span with the reasoning behind them: each flight's Why lists the notes whose span touches it,
// and Unplaced those that touch none (a run dropped for want of a position, evidence set aside).
type Explanation struct {
	Segments []Segment `json:"flights"`
	Unplaced []Note    `json:"unplaced,omitempty"`
}

// Explain is Segments with the notes of how each flight came about.
func Explain(st *store.Store, hexAddr string, since, until time.Time) (Explanation, error) {
	tr := &trace{}
	segs, err := segments(st, hexAddr, since.Add(-Lookback), until, tr, false)
	if err != nil {
		return Explanation{}, err
	}
	out := Explanation{Segments: []Segment{}}
	for _, sg := range segs {
		if !sg.End.Before(since) {
			out.Segments = append(out.Segments, sg)
		}
	}
	for _, n := range tr.notes {
		placed := false
		for i := range out.Segments {
			sg := &out.Segments[i]
			slack := n.slack()
			if !n.To.Before(sg.Start.Add(-slack)) && !n.From.After(sg.End.Add(slack)) {
				sg.Why = append(sg.Why, n)
				placed = true
			}
		}
		if !placed && !n.To.Before(since) {
			out.Unplaced = append(out.Unplaced, n)
		}
	}
	return out, nil
}
