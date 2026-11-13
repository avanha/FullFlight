package decoder

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// faultRe is an Airbus failure report: FLR/PN<part>/FR<yymmddhhmm>00<ata 6 digits><phase 2 digits><failed system>/ID<message>.
var faultRe = regexp.MustCompile(`(?s)^FLR/(?:PN([A-Z0-9]+)/)?FR(\d{10})00(\d{6})(\d{2})(.*?)\s*/ID(.*?)\s*$`)

// decodeFault reads a failure report. The ATA chapter number is read from the digits after the time: they matched the
// system named in three of the four samples (hydraulics 29, ADIRU 34, toilet 38), but the flight phase number is not understood.
func decodeFault(m Message) (Result, bool) {
	text := strings.TrimSpace(m.Text)
	g := faultRe.FindStringSubmatch(text)
	if g == nil {
		return Result{}, false
	}
	at, err := time.Parse("0601021504", g[2])
	if err != nil {
		return Result{}, false
	}
	system, id := strings.Join(strings.Fields(g[5]), " "), strings.Join(strings.Fields(g[6]), " ")
	ata := g[3][:2] + "-" + g[3][2:4] + "-" + g[3][4:]
	r := Result{Kind: KindFault, Label: m.Label, Dir: m.Dir, Body: text, Fields: map[string]any{
		"part": g[1], "at": at, "ata": ata, "phase": g[4], "system": system, "id": id,
	}}
	if r.Dir == Unknown {
		r.Dir = Downlink
	}
	s := fmt.Sprintf("Fault report: %s", system)
	if id != "" {
		s += " (" + id + ")"
	}
	s += fmt.Sprintf(", ATA %s, at %s", ata, at.Format("2006-01-02 15:04Z"))
	r.Summary = s
	r.Text = s
	if g[1] != "" {
		r.Text += "\nPart number " + g[1]
	}
	r.Text += "\nFlight phase " + g[4] + " (not decoded)"
	return r, true
}
