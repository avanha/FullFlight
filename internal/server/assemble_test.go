package server

import (
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// The three blocks of a United performance report (the third is the end), as a station heard them and with no joined copy.
const (
	apmBlock1 = "APM    1 N24993         UAL182  KDENEDDF211026232013,\r\n,70000021,.843,,,274.5,,,-13.07,,,37001,,,485547,,,6483,6485,,,\r\n 92.26, 92.25,,,  1.069,545.7,,,\r\n 61.5, 60.9, 81.5, 80.9,,,0,0,1,,,,,, 43.7566,,,\r\n -"
	apmBlock2 = "92.7391,,,  71.0,,,  4.7,,,  75.4,,,  -0.001, 0.006,    0,,,\r\n 6.673,  15, 55543,36600,36900,,,,,,10553,10584,,,  4.3,\r\n 44, 41, 37, 42,505800,,, 0.0),,,355800,,, 76300,36700,37000,\r\n,,,,,958959,958974,,,,2"
	apmBlock3 = "3.30,,,,,,105.8,106.0,,,\r\n,,,,803.1,811.2,,,,,,,-45.3,,, -2.2,,, -0.1,,,  2.7,\r\n,,  4.5, 6.4,,-11.2,-11.5,,, -0.7,,  7.6,  7.6,,, 0.0, 0.0,,,\r\n 0.00,,, 0.00,,, 0.00,,,,,  0.1\r\n"
)

func block(id int64, n int, at time.Time, text string) store.Message {
	return store.Message{ID: id, Hex: "A12797", T: at, Label: "H1", BlockID: string(rune('0' + n)), MsgNum: "D95", Station: "76-EDDF1-VDLM", Direction: "down", Text: text, Kind: "unknown"}
}

func TestBlocksWithNoJoinedCopyAreJoined(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 21, 24, 15, 0, time.UTC)
	ms := []store.Message{block(11, 1, t0, apmBlock1), block(12, 2, t0.Add(time.Second), apmBlock2), block(13, 3, t0.Add(3*time.Second), apmBlock3)}
	got := assembleBlocks(ms)
	if len(got) != 1 {
		t.Fatalf("assembled %d messages, want 1", len(got))
	}
	v := got[0]
	if v.ID != -11 || v.Text != apmBlock1+apmBlock2+apmBlock3 || v.Kind != "ops_report" || !strings.Contains(v.Summary, "performance report KDEN→EDDF") || !strings.Contains(v.Summary, "43.757N 92.739W") {
		t.Errorf("assembled = %+v", v)
	}
	// the blocks then fold under it
	if partOf, parts := foldParts(append(ms, v)); partOf[11] != -11 || partOf[13] != -11 || len(parts[-11]) != 3 {
		t.Errorf("partOf %v parts %v", partOf, parts)
	}

	// a block we never heard: the run stops there, and one block alone is not a message
	ms = []store.Message{block(11, 1, t0, apmBlock1), block(12, 2, t0.Add(time.Second), apmBlock2), block(14, 4, t0.Add(4*time.Second), apmBlock3)}
	if got := assembleBlocks(ms); len(got) != 1 || got[0].Text != apmBlock1+apmBlock2 {
		t.Errorf("with block 3 missing: %+v", got)
	}

	// a joined copy that is already there is all there is to say
	whole := block(13, 3, t0.Add(3*time.Second), apmBlock1+apmBlock2+apmBlock3)
	if got := assembleBlocks([]store.Message{block(11, 1, t0, apmBlock1), block(12, 2, t0.Add(time.Second), apmBlock2), whole}); len(got) != 0 {
		t.Errorf("joined again: %+v", got)
	}
}
