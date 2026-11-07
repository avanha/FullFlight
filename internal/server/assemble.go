package server

import (
	"sort"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/decoder"
	"github.com/avanha/fullflight/internal/store"
)

// A long message is sent in blocks of up to about 220 characters, and airframes.io usually also gives it joined up under the last
// block (see foldParts). When it does not, the blocks are all there is, and the later ones are undecodable on their own: they have
// no header. assembleBlocks joins them here, for reading, into a message of its own (a negative id: it is not a row). Only a run
// of blocks with nothing missing is joined: the block numbers go round 0-9 or A-Z, so a skipped number means a block we never heard,
// and joining across it would run unrelated data together.
const (
	blockGap  = 90 * time.Second // blocks of one message come seconds apart
	blockFull = 150              // a block shorter than this ends its message
)

// nextBlock is the block number that follows b, ok false for one we do not know the order of.
func nextBlock(b string) (string, bool) {
	if len(b) != 1 {
		return "", false
	}
	switch c := b[0]; {
	case c >= '0' && c < '9', c >= 'A' && c < 'Z':
		return string(c + 1), true
	case c == '9':
		return "0", true
	case c == 'Z':
		return "A", true
	}
	return "", false
}

// assembleBlocks returns the messages that can be put together from blocks that no joined copy covers. ms are the messages as stored.
func assembleBlocks(ms []store.Message) []store.Message {
	partOf, parts := foldParts(ms)
	groups := map[string][]store.Message{}
	for _, m := range ms {
		if m.MsgNum == "" || m.BlockID == "" || partOf[m.ID] != 0 || len(parts[m.ID]) > 0 {
			continue
		}
		k := strings.Join([]string{m.Hex, m.Station, m.MsgNum, m.Label}, "/")
		groups[k] = append(groups[k], m)
	}
	var out []store.Message
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].T.Before(g[j].T) || g[i].T.Equal(g[j].T) && g[i].ID < g[j].ID })
		start := 0
		for i := 1; i <= len(g); i++ {
			if i < len(g) {
				prev := g[i-1]
				if next, ok := nextBlock(prev.BlockID); ok && next == g[i].BlockID && g[i].T.Sub(prev.T) <= blockGap && len(prev.Text) >= blockFull {
					continue
				}
			}
			if i-start >= 2 {
				if v, ok := joinBlocks(g[start:i]); ok {
					out = append(out, v)
				}
			}
			start = i
		}
	}
	return out
}

// joinBlocks makes one message of a run of blocks and reads it.
func joinBlocks(run []store.Message) (store.Message, bool) {
	first, last := run[0], run[len(run)-1]
	var text strings.Builder
	for _, b := range run {
		text.WriteString(b.Text)
	}
	dir := decoder.Unknown
	switch first.Direction {
	case "up":
		dir = decoder.Uplink
	case "down":
		dir = decoder.Downlink
	}
	res, err := decoder.Decode(decoder.Message{Label: first.Label, Dir: dir, Text: text.String()})
	if err != nil {
		return store.Message{}, false
	}
	v := store.Message{
		ID: -first.ID, Hex: first.Hex, T: last.T, Label: first.Label, MsgNum: first.MsgNum, Direction: first.Direction, SourceType: first.SourceType,
		Station: first.Station, Freq: first.Freq, Text: text.String(), RxCount: 1, BlockID: last.BlockID,
		Kind: string(res.Kind), Tail: res.Identity.Tail, Callsign: res.Identity.Callsign, Flight: res.Identity.Flight, Summary: res.Summary, Detail: res.Text,
		AFTail: first.AFTail, AFFlight: first.AFFlight,
	}
	return v, true
}
