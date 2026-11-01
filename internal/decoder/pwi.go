package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PWI is a predicted-winds message: the winds the flight computer asks for along its route (REQPWI), the table the
// airline sends back (PWI/WD...), and the receipt for it (RESPWI). Read from samples, not a specification.
//
//	REQPWI/TS054236,301026/WQ340.360.380:RW26L.UQQ.ORNAI.NATES/DQ340/CQ340   request: time and date, levels, route
//	- #M1PWI/WD400,ECOMI,278067,400M54.NATES,251091,400M53/WD380,...         winds per level: fix, direction and speed, level and temperature
//	PWI/WD390,JAGWA,275040.BURGL,288047/DD300274028.200234019                  the aircraft's own, with descent winds
//	RESPWI/AK,71605F                                                           AK acknowledged, AC accepted, RJ rejected
//
// An uplink is cut into blocks, each starting "- #M" and a character, which can fall in the middle of a word. The
// last four characters are a checksum.
type PWI struct {
	What     string      `json:"what"`               // request, response or winds
	Response string      `json:"response,omitempty"` // acknowledged, accepted or rejected
	Time     string      `json:"time,omitempty"`     // when a request was made, hh:mm:ss dd/mm/yy
	Valid    string      `json:"valid,omitempty"`    // the time the forecast is for, yy-mm-dd hh:mm
	Levels   []WindLevel `json:"levels,omitempty"`   // winds at each flight level along the route
	Climb    []LevelWind `json:"climb,omitempty"`
	Descent  []LevelWind `json:"descent,omitempty"`
	ReqFLs   []int       `json:"req_levels,omitempty"` // the levels a request asks about
	Route    []string    `json:"route,omitempty"`      // the fixes a request asks about
	ClimbFL  int         `json:"climb_fl,omitempty"`
	DescFL   int         `json:"descent_fl,omitempty"`
}

// WindLevel is the winds at one flight level, fix by fix.
type WindLevel struct {
	FL     int         `json:"fl"`
	Points []WindPoint `json:"points"`
}

// WindPoint is the wind at a fix: where it blows from, how fast, and the temperature when the table gives it.
type WindPoint struct {
	Fix   string `json:"fix"`
	Dir   int    `json:"dir"`
	Speed int    `json:"speed"`
	TempC *int   `json:"temp_c,omitempty"`
}

// LevelWind is a wind at a flight level, not tied to a fix (climb and descent winds).
type LevelWind struct {
	FL    int `json:"fl"`
	Dir   int `json:"dir"`
	Speed int `json:"speed"`
}

var (
	pwiHead    = regexp.MustCompile(`^(?:[A-Z0-9]{10})?(?:-#M[A-Z0-9]{1,2}|#M[A-Z0-9]{1,2})?(REQ|RES)?PWI/(.+)$`)
	pwiBlock   = regexp.MustCompile(`-#M[A-Z0-9]`)
	pwiCRC     = regexp.MustCompile(`[0-9A-F]{4}$`)
	pwiWind    = regexp.MustCompile(`^(\d{3})(\d{2,3})$`)
	pwiTemp    = regexp.MustCompile(`^(\d{3})([MP])(\d{2})$`)
	pwiLevelWd = regexp.MustCompile(`^(\d{3})(\d{3})(\d{3})`)
)

// ParsePWI decodes a predicted-winds message, or reports that the text is not one.
func ParsePWI(text string) (PWI, bool) {
	t := strings.Join(strings.Fields(text), "")
	m := pwiHead.FindStringSubmatch(t)
	if m == nil {
		return PWI{}, false
	}
	// the blocks of a long uplink were glued with their headers still in place
	body := pwiCRC.ReplaceAllString(pwiBlock.ReplaceAllString(m[2], ""), "")
	p := PWI{What: "winds"}
	switch m[1] {
	case "REQ":
		p.What = "request"
	case "RES":
		p.What = "response"
	}
	for _, sec := range strings.Split(body, "/") {
		switch {
		case p.What == "response" && len(sec) > 2 && (sec[:2] == "AK" || sec[:2] == "AC" || sec[:2] == "RJ"):
			p.Response = map[string]string{"AK": "acknowledged", "AC": "accepted", "RJ": "rejected"}[sec[:2]]
		case strings.HasPrefix(sec, "TS"):
			f := strings.Split(sec[2:], ",")
			if len(f) == 2 && len(f[0]) == 6 && len(f[1]) == 6 {
				p.Time = fmt.Sprintf("%s:%s:%s %s/%s/%s", f[0][:2], f[0][2:4], f[0][4:], f[1][:2], f[1][2:4], f[1][4:])
			}
		case strings.HasPrefix(sec, "SN") && len(sec) == 12:
			p.Valid = fmt.Sprintf("%s-%s-%s %s:%s", sec[2:4], sec[4:6], sec[6:8], sec[8:10], sec[10:])
		case strings.HasPrefix(sec, "WQ"):
			if lv, route, ok := strings.Cut(sec[2:], ":"); ok {
				for _, s := range strings.Split(lv, ".") {
					if n, err := strconv.Atoi(s); err == nil {
						p.ReqFLs = append(p.ReqFLs, n)
					}
				}
				p.Route = strings.Split(route, ".")
			}
		case strings.HasPrefix(sec, "CQ"):
			p.ClimbFL, _ = strconv.Atoi(sec[2:])
		case strings.HasPrefix(sec, "DQ"):
			p.DescFL, _ = strconv.Atoi(sec[2:])
		case strings.HasPrefix(sec, "WD") && len(sec) > 6 && sec[5] == ',':
			fl, err := strconv.Atoi(sec[2:5])
			if err != nil {
				continue
			}
			l := WindLevel{FL: fl}
			for _, e := range strings.Split(sec[6:], ".") {
				if w, ok := windPoint(e); ok {
					l.Points = append(l.Points, w)
				}
			}
			p.Levels = append(p.Levels, l)
		case strings.HasPrefix(sec, "DD"):
			p.Descent = levelWinds(sec[2:])
		case strings.HasPrefix(sec, "CB"):
			p.Climb = levelWinds(sec[2:])
		}
	}
	if p.What == "winds" && len(p.Levels) == 0 && len(p.Climb) == 0 && len(p.Descent) == 0 {
		return PWI{}, false
	}
	return p, true
}

func windPoint(e string) (WindPoint, bool) {
	f := strings.Split(e, ",")
	if len(f) < 2 || f[0] == "" {
		return WindPoint{}, false
	}
	m := pwiWind.FindStringSubmatch(f[1])
	if m == nil {
		return WindPoint{}, false
	}
	w := WindPoint{Fix: f[0]}
	w.Dir, _ = strconv.Atoi(m[1])
	w.Speed, _ = strconv.Atoi(m[2])
	if len(f) > 2 {
		if t := pwiTemp.FindStringSubmatch(f[2]); t != nil {
			c, _ := strconv.Atoi(t[3])
			if t[2] == "M" {
				c = -c
			}
			w.TempC = &c
		}
	}
	return w, true
}

// levelWinds reads "300274028.200234019": flight level, direction and speed, three digits each.
func levelWinds(s string) []LevelWind {
	var out []LevelWind
	for _, e := range strings.Split(s, ".") {
		if m := pwiLevelWd.FindStringSubmatch(e); m != nil {
			var w LevelWind
			w.FL, _ = strconv.Atoi(m[1])
			w.Dir, _ = strconv.Atoi(m[2])
			w.Speed, _ = strconv.Atoi(m[3])
			out = append(out, w)
		}
	}
	return out
}

func decodePWI(m Message) (Result, bool) {
	p, ok := ParsePWI(m.Text)
	if !ok {
		return Result{}, false
	}
	r := Result{Kind: KindPWI, Label: m.Label, Dir: m.Dir, Body: strings.TrimSpace(m.Text), Fields: map[string]any{"pwi": p}}
	if r.Label == "" {
		r.Label = "H1"
	}
	if r.Dir == Unknown {
		switch {
		case p.What != "winds":
			r.Dir = Downlink
		case strings.HasPrefix(strings.TrimSpace(m.Text), "-"):
			r.Dir = Uplink
		}
	}
	r.Summary = p.summary()
	r.Text = r.Summary
	for _, l := range p.Levels {
		var pts []string
		for _, w := range l.Points {
			s := fmt.Sprintf("%s %03d°/%d", w.Fix, w.Dir, w.Speed)
			if w.TempC != nil {
				s += fmt.Sprintf(" %d°C", *w.TempC)
			}
			pts = append(pts, s)
		}
		r.Text += fmt.Sprintf("\nFL%d: %s", l.FL, strings.Join(pts, ", "))
	}
	for _, g := range []struct {
		name string
		w    []LevelWind
	}{{"Climb", p.Climb}, {"Descent", p.Descent}} {
		if len(g.w) == 0 {
			continue
		}
		var s []string
		for _, w := range g.w {
			s = append(s, fmt.Sprintf("FL%d %03d°/%d", w.FL, w.Dir, w.Speed))
		}
		r.Text += fmt.Sprintf("\n%s winds: %s", g.name, strings.Join(s, ", "))
	}
	if len(p.Route) > 0 {
		r.Text += "\nRoute: " + strings.Join(p.Route, " ")
	}
	return r, true
}

func flList(fls []int) string {
	s := make([]string, len(fls))
	for i, f := range fls {
		s[i] = strconv.Itoa(f)
	}
	return "FL" + strings.Join(s, "/")
}

func (p PWI) summary() string {
	switch p.What {
	case "response":
		s := "Wind data " + p.Response
		if p.Response == "" {
			s = "Wind data response"
		}
		if p.Time != "" {
			s += " (request of " + p.Time + ")"
		}
		return s
	case "request":
		s := "Wind request"
		if len(p.ReqFLs) > 0 {
			s += " " + flList(p.ReqFLs)
		}
		if n := len(p.Route); n > 0 {
			s += fmt.Sprintf(", %d fixes %s → %s", n, p.Route[0], p.Route[n-1])
		}
		if len(p.ReqFLs) == 0 && (p.ClimbFL > 0 || p.DescFL > 0) {
			s += fmt.Sprintf(": climb FL%d, descent FL%d", p.ClimbFL, p.DescFL)
		}
		return s
	}
	s := "Predicted winds"
	if len(p.Levels) > 0 {
		fls := make([]int, len(p.Levels))
		for i, l := range p.Levels {
			fls[i] = l.FL
		}
		s += " " + flList(fls)
		if pts := p.Levels[0].Points; len(pts) > 0 {
			s += fmt.Sprintf(", %d fixes %s → %s", len(pts), pts[0].Fix, pts[len(pts)-1].Fix)
		}
	}
	if len(p.Descent) > 0 {
		s += ", descent winds"
	}
	if p.Valid != "" {
		s += " valid " + p.Valid + "Z"
	}
	return s
}
