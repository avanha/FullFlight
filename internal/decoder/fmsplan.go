package decoder

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/avanha/fullflight/internal/flightplan"
)

// A Boeing FMS sends its plan with the position report that starts it, in pages of about 780 characters (three or four blocks
// each) that airframes.io puts back together. Read from N956AK's reports and checked against its track and landing:
//
//	POSN28258W082126,RDSOX,230650,181,JFRYS,...,73/RI:DA:KMCO:AA:KSEA(16R):D:RDSOX1.JFRYS:F:JFRYS..PATOY.Q116.JAWJA..MGM:A:GLASR3.MLP:F:VECTOR..DISCO..CIBUX:AP:ILS 16R.CIBUX:F:ZERLI
//	/PR1782,217,340,344,,45,15,289038,M44,180,P0,P0
//	/FHJFRYS,230853,290K,2065,341,M11,290050,409,Y,271,16.PATOY,231512,290K,2759,333,M28,293047,454,N,329,44.DEANR,...
//	/PG69,1
//
// The first page is the position report, the route (the same as a filed plan's) and the forecast for each waypoint: its name, the
// ETA (hhmmss), the speed (knots, with a K), the altitude (tens of feet), the fuel (hundreds of kg), the temperature, the wind
// (three digits of direction and three of speed), the true airspeed, a flag and the track and distance on to the next. Later pages
// carry the rest of the list, cut anywhere, and the last ends with the arrival (/DTKSEA,16R,79,042536,73: airport, runway, a number,
// the ETA at the runway, a number). Only the name, ETA and altitude are read.
type FMSETA struct {
	Name  string  `json:"name"`
	ETA   string  `json:"eta"` // hhmmss UTC
	AltFt float64 `json:"alt_ft,omitempty"`
}

// fmsETARe is a waypoint's record: a name, an ETA and a speed.
var (
	fmsETARe  = regexp.MustCompile(`^([A-Z0-9]{2,6}),(\d{6}),\d{2,3}K,(\d+),`)
	fmsPageRe = regexp.MustCompile(`/PG\d+,(\d)[0-9A-F]{0,6}$`)
)

// fmsETAs reads the waypoint records of a list; a record cut off by the page, or without a forecast (a name alone), is left out.
func fmsETAs(list string) []FMSETA {
	var out []FMSETA
	for _, rec := range strings.Split(list, ".") {
		if m := fmsETARe.FindStringSubmatch(rec); m != nil && timeOfDay(m[2]) != nil {
			out = append(out, FMSETA{Name: m[1], ETA: m[2], AltFt: atof(m[3]) * 10})
		}
	}
	return out
}

// fmsSection is the text of the section that begins with the given tag, up to the next "/".
func fmsSection(body, tag string) (string, bool) {
	for _, sec := range strings.Split(body, "/") {
		if strings.HasPrefix(sec, tag) {
			return sec[len(tag):], true
		}
	}
	return "", false
}

// fmsPlanPart adds to an FMS position report the plan and forecast it carries, if it has them.
func fmsPlanPart(body string, r *Result) {
	if ri, ok := fmsSection(body, "RI:"); ok {
		if p, ok := flightplan.ParseRI("RI:" + ri); ok {
			r.Fields["plan"] = p
			r.Fields["fms_dest"] = p.To
		}
	}
	list, ok := fmsSection(body, "FH")
	if !ok {
		return
	}
	etas := fmsETAs(list)
	if len(etas) == 0 {
		return
	}
	r.Fields["fms_etas"] = etas
	// The waypoints in the next two hours, which the map can place beside the aircraft by name (a name far away is too easily
	// another place of the same name).
	first := timeOfDay(etas[0].ETA)
	prev := *first
	elapsed := 0.0
	named := map[string]bool{} // the position report names the first few already
	for _, f := range r.Fixes {
		named[f.Name] = true
	}
	for _, e := range etas {
		t := *timeOfDay(e.ETA)
		if t < prev {
			elapsed += 86400
		}
		prev = t
		if elapsed+t-*first > 2*3600 {
			break
		}
		if named[e.Name] {
			continue
		}
		secs, alt := t, e.AltFt
		r.Fixes = append(r.Fixes, FixRef{Name: e.Name, Role: "ahead", Secs: &secs, AltFt: &alt})
	}
}

// fmsPlanSummary is what a position report with a plan adds to its line.
func fmsPlanSummary(r Result) string {
	p, _ := r.Fields["plan"].(*flightplan.Plan)
	if p == nil {
		return ""
	}
	s := fmt.Sprintf("; plan %s→%s", p.From, p.To)
	if p.Runway != "" {
		s += " runway " + p.Runway
	}
	return s + fmt.Sprintf(", %d fixes", len(p.Fixes))
}

// decodeFMSPage reads a later page of an FMS plan report: the waypoint list carried on, and on the last page the arrival.
func decodeFMSPage(m Message) (Result, bool) {
	t := strings.TrimSpace(strings.ReplaceAll(m.Text, "\r\n", ""))
	pg := fmsPageRe.FindStringSubmatch(t)
	if pg == nil || strings.HasPrefix(t, "POS") {
		return Result{}, false
	}
	list := strings.SplitN(t, "/", 2)[0]
	etas := fmsETAs(list)
	if len(etas) == 0 {
		return Result{}, false
	}
	r := Result{Kind: KindProgress, Label: m.Label, Dir: m.Dir, Body: t, Fields: map[string]any{"fms_etas": etas, "fms_page": atoi(pg[1])}}
	if r.Dir == Unknown {
		r.Dir = Downlink
	}
	if r.Label == "" {
		r.Label = "H1"
	}
	s := fmt.Sprintf("FMS route forecast, page %s: %s %s to %s %s", pg[1], etas[0].Name, hmZ(etas[0].ETA[:4]), etas[len(etas)-1].Name, hmZ(etas[len(etas)-1].ETA[:4]))
	if dt, ok := fmsSection(t, "DT"); ok {
		f := strings.Split(dt, ",")
		if len(f) > 3 && icaoOK(f[0]) && len(f[3]) == 6 {
			g := &flightplan.Progress{To: f[0], Runway: f[1], ETA: f[3]}
			r.Fields["progress"] = g
			s += fmt.Sprintf("; arrives %s runway %s at %s", g.To, g.Runway, hmZ(g.ETA[:4]))
		}
	}
	r.Summary, r.Text = s, s
	if len(etas) > 1 {
		var lines []string
		for _, e := range etas {
			lines = append(lines, fmt.Sprintf("%s %s:%s:%sZ, %s ft", e.Name, e.ETA[:2], e.ETA[2:4], e.ETA[4:], groupThousands(int(e.AltFt))))
		}
		r.Text += "\n" + strings.Join(lines, "\n")
	}
	return r, true
}

func icaoOK(s string) bool { return len(s) == 4 && strings.ToUpper(s) == s }
