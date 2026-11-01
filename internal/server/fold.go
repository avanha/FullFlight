package server

import (
	"sort"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// A message sent in several blocks reaches us as the blocks and, usually, as one copy joined up (airframes.io
// stores that under the last block). The blocks of such a message, and the shorter copies of it, are the same message
// again: they are listed under the whole one, not as rows of their own.
const (
	foldWithin  = 3 * time.Minute // blocks of one message arrive within seconds of each other
	foldMinText = 8               // a few characters are in everything
)

var squash = strings.NewReplacer(" ", "", "\r", "", "\n", "", "\t", "")

// foldParts finds the messages whose text is wholly inside a longer copy of the same message number, received within a
// few minutes of it: it returns each such message's whole copy, and each whole copy's parts, oldest first.
func foldParts(ms []store.Message) (partOf map[int64]int64, parts map[int64][]int64) {
	type entry struct {
		m    store.Message
		text string
	}
	groups := map[string][]entry{}
	for _, m := range ms {
		if m.MsgNum == "" {
			continue
		}
		if t := squash.Replace(m.Text); len(t) >= foldMinText {
			groups[m.Hex+"/"+m.MsgNum] = append(groups[m.Hex+"/"+m.MsgNum], entry{m, t})
		}
	}
	whole := map[int64]int64{}
	for _, g := range groups {
		for _, f := range g {
			var best *entry
			for i := range g {
				p := &g[i]
				d := p.m.T.Sub(f.m.T)
				if p.m.ID == f.m.ID || d > foldWithin || -d > foldWithin || !strings.Contains(p.text, f.text) {
					continue
				}
				// the longer one is the whole; of equal copies, the first
				if len(p.text) < len(f.text) || len(p.text) == len(f.text) && p.m.ID > f.m.ID {
					continue
				}
				if best == nil || len(p.text) > len(best.text) || len(p.text) == len(best.text) && p.m.ID < best.m.ID {
					best = p
				}
			}
			if best != nil {
				whole[f.m.ID] = best.m.ID
			}
		}
	}
	partOf, parts = map[int64]int64{}, map[int64][]int64{}
	for id, p := range whole {
		for seen := 0; whole[p] != 0 && seen < 10; seen++ { // a part of a part belongs to the whole of it
			p = whole[p]
		}
		partOf[id] = p
		parts[p] = append(parts[p], id)
	}
	at := map[int64]time.Time{}
	for _, m := range ms {
		at[m.ID] = m.T
	}
	for _, ids := range parts {
		sort.Slice(ids, func(i, j int) bool {
			return at[ids[i]].Before(at[ids[j]]) || at[ids[i]].Equal(at[ids[j]]) && ids[i] < ids[j]
		})
	}
	return partOf, parts
}
