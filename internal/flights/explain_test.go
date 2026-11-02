package flights

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

func explain(t *testing.T, r *rig) Explanation {
	t.Helper()
	ex, err := Explain(r.st, hex, day0.Add(-24*time.Hour), day0.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

func rules(ns []Note) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Rule)
	}
	return strings.Join(out, ",")
}

// Explaining changes nothing about the flights, and says what was done: QF1's stop that no fix showed, where it was cut and where
// the second leg's origin came from, and why the two legs of one number are not one flight.
func TestExplainSaysWhatWasDone(t *testing.T) {
	r := newRig(t)
	r.said(at(0, 5, 30), "QF1")
	r.route(at(0, 5, 30), "YSSY", "WSSS")
	r.air(at(0, 6, 40), "QFA1", -33.4, 150.5, 30000, false)
	for _, h := range []int{8, 10, 12, 14} {
		r.said(at(0, h, 0), "QF1")
	}
	for i, alt := range []float64{1575, 3150, 7000, 12000, 17000} {
		r.air(at(0, 16, 36).Add(time.Duration(i)*2*time.Minute), "QFA1", 1.40+float64(i)*0.1, 104.0-float64(i)*0.1, alt, false)
	}
	plain := r.segments()
	ex := explain(t, r)
	if len(ex.Segments) != 2 || len(plain) != 2 {
		t.Fatalf("%d and %d flights", len(ex.Segments), len(plain))
	}
	for i := range plain {
		got := ex.Segments[i]
		got.Why = nil
		if !reflect.DeepEqual(got, plain[i]) {
			t.Errorf("explaining changed flight %d:\n%+v\n%+v", i, got, plain[i])
		}
		if plain[i].Why != nil {
			t.Errorf("the usual run keeps no notes: %+v", plain[i].Why)
		}
	}
	a, b := ex.Segments[0], ex.Segments[1]
	for _, want := range []string{"run", "route", "split", "unseen-stop", "not-joined"} {
		if !strings.Contains(rules(a.Why), want) {
			t.Errorf("the first leg lacks %q: %s", want, rules(a.Why))
		}
	}
	if !strings.Contains(rules(b.Why), "seen-origin") || !strings.Contains(rules(b.Why), "unseen-stop") {
		t.Errorf("the second leg: %s", rules(b.Why))
	}
	for _, n := range b.Why {
		if n.Rule == "seen-origin" && !strings.Contains(n.Detail, "WSSS") {
			t.Errorf("seen-origin says %q", n.Detail)
		}
		if n.Rule == "unseen-stop" && !strings.Contains(n.Detail, "WSSS") {
			t.Errorf("unseen-stop says %q", n.Detail)
		}
	}
	for _, n := range a.Why {
		if n.Rule == "not-joined" && !strings.Contains(n.Detail, "stopped on the ground") {
			t.Errorf("not-joined says %q", n.Detail)
		}
		if n.Rule == "route" && !strings.Contains(n.Detail, "YSSY > WSSS") {
			t.Errorf("route says %q", n.Detail)
		}
	}
}

// A sighting set aside, a stray folded into the flight around it, and a run dropped for want of a position each leave a note, and
// those that belong to no flight are listed apart.
func TestExplainSaysWhatWasSetAside(t *testing.T) {
	r := newRig(t)
	// UA100 flying, with the airline's message about UA200 inside it and a stray sighting of a third number
	for i := 0; i < 8; i++ {
		r.adsb(at(0, 10, 0).Add(time.Duration(i)*10*time.Minute), "UAL100")
	}
	r.said(at(0, 10, 5), "UA100")
	r.msg(store.Message{T: at(0, 10, 25), Kind: "ops_report", Flight: "UA200"})
	r.said(at(0, 10, 45), "UA100")
	r.said(at(0, 11, 10), "UA555")
	r.said(at(0, 11, 20), "UA100")
	// a number heard once, hours after, with no position near it
	r.said(at(1, 20, 0), "UA300")
	ex := explain(t, r)
	if len(ex.Segments) != 1 || ex.Segments[0].Flight != "UA100" {
		t.Fatalf("%+v", ex.Segments)
	}
	got := rules(ex.Segments[0].Why)
	for _, want := range []string{"weak", "stray", "run"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q note: %s", want, got)
		}
	}
	for _, n := range ex.Segments[0].Why {
		if n.Rule == "weak" && !strings.Contains(n.Detail, "UA200") {
			t.Errorf("weak says %q", n.Detail)
		}
	}
	if !strings.Contains(rules(ex.Unplaced), "dropped") {
		t.Errorf("a number with no position is dropped and listed apart: %s", rules(ex.Unplaced))
	}
}
