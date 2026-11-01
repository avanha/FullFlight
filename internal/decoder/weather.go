package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// WeatherReport is what an ATIS or METAR says about the weather at an airport, read from the usual groups:
// observation time, wind, visibility, cloud, temperature and dew point, altimeter. Any of them can be missing.
type WeatherReport struct {
	Kind    string   `json:"kind"`              // ATIS or METAR
	Airport string   `json:"airport,omitempty"` // ICAO
	Type    string   `json:"type,omitempty"`    // an ATIS: arrival or departure
	Info    string   `json:"info,omitempty"`    // an ATIS: the information letter
	Time    string   `json:"time,omitempty"`    // hhmm UTC of the observation
	WindDir *int     `json:"wind_dir,omitempty"`
	WindKt  *int     `json:"wind_kt,omitempty"`
	GustKt  int      `json:"gust_kt,omitempty"`
	Vis     string   `json:"vis,omitempty"`
	Sky     []string `json:"sky,omitempty"`
	TempC   *int     `json:"temp_c,omitempty"`
	DewC    *int     `json:"dew_c,omitempty"`
	QNH     string   `json:"qnh,omitempty"` // 29.88 (inches) or 1013 (hectopascals)
}

var (
	wxTimeRe = regexp.MustCompile(`\b(\d{4})Z\b`)
	wxWindRe = regexp.MustCompile(`\b(\d{3}|VRB)(\d{2,3})(?:G(\d{2,3}))?KT\b`)
	wxVisRe  = regexp.MustCompile(`\b(\d{1,2}(?: \d/\d)?|\d/\d)SM\b`)
	wxSkyRe  = regexp.MustCompile(`\b(?:(?:FEW|SCT|BKN|OVC|VV)\d{3}(?:CB|TCU)?|CLR|SKC|CAVOK)\b`)
	wxTempRe = regexp.MustCompile(`\b(M?\d{2})/(M?\d{2})\b`)
	wxAltRe  = regexp.MustCompile(`\b([AQ])(\d{4})\b`)
)

func wxTemp(s string) *int {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "M"))
	if err != nil {
		return nil
	}
	if strings.HasPrefix(s, "M") {
		n = -n
	}
	return &n
}

// readWeather fills in what the text says; ok is false if it has no wind, temperature or altimeter at all.
func readWeather(text string) (w WeatherReport, ok bool) {
	text = spaces.ReplaceAllString(text, " ")
	if m := wxTimeRe.FindStringSubmatch(text); m != nil {
		w.Time = hhmm(m[1])
	}
	rest := text
	if m := wxWindRe.FindStringSubmatchIndex(text); m != nil {
		g := wxWindRe.FindStringSubmatch(text)
		if g[1] != "VRB" {
			d := atoi(g[1])
			w.WindDir = &d
		}
		k := atoi(g[2])
		w.WindKt = &k
		w.GustKt = atoi(g[3])
		rest = text[m[1]:]
		ok = true
	}
	if m := wxVisRe.FindStringSubmatch(rest); m != nil {
		w.Vis = m[1] + " SM"
	}
	w.Sky = wxSkyRe.FindAllString(rest, -1)
	if m := wxTempRe.FindStringSubmatch(rest); m != nil {
		w.TempC, w.DewC = wxTemp(m[1]), wxTemp(m[2])
		ok = true
	}
	if m := wxAltRe.FindStringSubmatch(rest); m != nil {
		if m[1] == "A" {
			w.QNH = m[2][:2] + "." + m[2][2:]
		} else {
			w.QNH = m[2]
		}
		ok = true
	}
	return w, ok
}

// describe is the weather in a line: 060°/8 kt, 10 SM, FEW022 BKN030 OVC060, 26/22 °C, QNH 29.88.
func (w WeatherReport) describe() string {
	var s []string
	if w.WindKt != nil {
		dir := "VRB"
		if w.WindDir != nil {
			dir = fmt.Sprintf("%03d°", *w.WindDir)
		}
		wind := fmt.Sprintf("%s/%d kt", dir, *w.WindKt)
		if w.GustKt > 0 {
			wind += fmt.Sprintf(" gusting %d", w.GustKt)
		}
		s = append(s, wind)
	}
	if w.Vis != "" {
		s = append(s, w.Vis)
	}
	if len(w.Sky) > 0 {
		s = append(s, strings.Join(w.Sky, " "))
	}
	if w.TempC != nil && w.DewC != nil {
		s = append(s, fmt.Sprintf("%d/%d °C", *w.TempC, *w.DewC))
	}
	if w.QNH != "" {
		s = append(s, "QNH "+w.QNH)
	}
	return strings.Join(s, ", ")
}

// An ATIS sent to the aircraft: the airport as three or four letters, ARR or DEP, and the information letter, then the
// broadcast ("1453Z - HNL ATIS INFO P 1453Z. 06008KT 10SM FEW022 ...").
var (
	atisReplyRe = regexp.MustCompile(`^/[A-Z0-9]{4,7}\.TI2/([A-Z0-9]{3,4}) +(ARR|DEP)\w* +ATIS *(\w)?(?:\s|$)`)
	atisInfoRe  = regexp.MustCompile(`ATIS INFO (\w)\b`)
)

func readATIS(body string) (WeatherReport, bool) {
	m := atisReplyRe.FindStringSubmatch(body)
	if m == nil {
		return WeatherReport{}, false
	}
	w, _ := readWeather(body)
	w.Kind, w.Airport, w.Info = "ATIS", icaoOf(m[1]), m[3]
	w.Type = map[string]string{"ARR": "arrival", "DEP": "departure"}[m[2]]
	if i := atisInfoRe.FindStringSubmatch(body); i != nil {
		w.Info = i[1]
	}
	return w, true
}

func (w WeatherReport) atisSummary() string {
	s := fmt.Sprintf("D-ATIS %s information", w.Type)
	if w.Info != "" {
		s += " " + w.Info
	}
	s += " for " + w.Airport
	if d := w.describe(); d != "" {
		s += ": " + d
	}
	return s
}

// Weather text sent to the aircraft (label 3T): the airport, then METAR and TAF blocks, newest first. The first METAR is
// the one summarised.
var wxUplinkRe = regexp.MustCompile(`^([A-Z]{4}) +\w+ +METAR ([A-Z]{4}) (\d{6})Z?`)

func decodeWeather(m Message) (Result, bool) {
	if m.Label != "3T" {
		return Result{}, false
	}
	t := spaces.ReplaceAllString(strings.TrimSpace(m.Text), " ")
	h := wxUplinkRe.FindStringSubmatch(t)
	if h == nil {
		return Result{}, false
	}
	block := t
	if i := strings.Index(t[len(h[0]):], "METAR "); i >= 0 {
		block = t[:len(h[0])+i]
	}
	if i := strings.Index(block, " TAF "); i >= 0 {
		block = block[:i]
	}
	w, ok := readWeather(block)
	if !ok {
		return Result{}, false
	}
	w.Kind, w.Airport, w.Time = "METAR", h[2], hhmm(h[3][2:6])
	r := Result{Kind: KindWeather, Label: m.Label, Dir: m.Dir, Body: strings.TrimSpace(m.Text), Fields: map[string]any{"weather": w}}
	if r.Dir == Unknown {
		r.Dir = Uplink
	}
	r.Summary = fmt.Sprintf("METAR %s %s: %s", w.Airport, hmZ(w.Time), w.describe())
	r.Text = r.Summary
	return r, true
}

// A data link disconnect, ".DIS." behind the ground station's address: the aircraft's registration, a two character code
// whose meaning is not known (20 and 80 are seen) and the checksum.
var disRe = regexp.MustCompile(`^/([A-Z0-9]{7})\.DIS\.\.?([A-Z0-9-]{5,6}?)([0-9A-F]{2})([0-9A-F]{4})\s*$`)

func parseDisconnect(body string, r *Result) {
	m := disRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	r.Fields["gs_addr"], r.Fields["code"] = m[1], m[3]
	r.Identity.Tail = m[2]
}
