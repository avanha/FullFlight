package decoder

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// The A380's maintenance system report, on label H1, as Emirates and Qantas send it: <CMS><IH>8|00001|00001|A6-EEV|UAE3HJ|20261106|2241|...</IH>
// gives the tail, the flight and the moment, <CH>20261106|2239||YSSY|NZCH|...</CH> the route, and each <CI> an item: a title with
// the class of item (<T>Cockpit items|1</T>), then <FM>code|date|time|...|component|system|...</FM> and, when the item has an
// effect on the flight deck, <FDA>code|text</FDA>. The same design as the A350's XML report (see a350.go). Read from samples.
var (
	cmsHeadRe = regexp.MustCompile(`<IH>[^|<]*\|[^|<]*\|[^|<]*\|([^|<]*)\|([^|<]*)\|(\d{8})\|(\d{4})\|`)
	cmsRouteR = regexp.MustCompile(`<CH>\d{8}\|\d{4}\|[^|<]*\|([A-Z]{4})\|([A-Z]{4})\|`)
	cmsItemRe = regexp.MustCompile(`(?s)<CI\b[^>]*>(.*?)</CI>`)
	cmsTitle  = regexp.MustCompile(`<T>([^|<]*)`)
	cmsFM     = regexp.MustCompile(`<FM\b[^>]*>([^<]*)`)
	cmsFDA    = regexp.MustCompile(`<FDA>([^|<]*)\|([^<]*)</FDA>`)
)

// CMSItem is one fault or message of a CMS report.
type CMSItem struct {
	Class     string // "Cockpit items", "Cabin items"
	Code      string
	At        time.Time
	Component string
	System    string
	Advisory  string // what the flight deck is told, if anything
}

// CMSReport is an A380 maintenance system report.
type CMSReport struct {
	Tail, Callsign string
	Origin, Dest   string
	Time           time.Time
	Items          []CMSItem
}

func cmsTime(date, hms string) time.Time {
	t, err := time.Parse("20060102 150405", date+" "+hms)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ParseCMS reads a maintenance system report; a long one is cut into blocks, so what is there is read.
func ParseCMS(text string) (CMSReport, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "<CMS>") {
		return CMSReport{}, false
	}
	var r CMSReport
	if m := cmsHeadRe.FindStringSubmatch(text); m != nil {
		r.Tail, r.Callsign = strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		r.Time = cmsTime(m[3], m[4]+"00")
	}
	if m := cmsRouteR.FindStringSubmatch(text); m != nil {
		r.Origin, r.Dest = m[1], m[2]
	}
	for _, c := range cmsItemRe.FindAllStringSubmatch(text, -1) {
		var it CMSItem
		if m := cmsTitle.FindStringSubmatch(c[1]); m != nil {
			it.Class = m[1]
		}
		if m := cmsFM.FindStringSubmatch(c[1]); m != nil {
			f := strings.Split(m[1], "|")
			if len(f) >= 8 {
				it.Code, it.At, it.Component, it.System = f[0], cmsTime(f[1], f[2]), strings.Join(strings.Fields(html.UnescapeString(f[6])), " "), f[7]
			}
		}
		if m := cmsFDA.FindStringSubmatch(c[1]); m != nil {
			it.Advisory = strings.Join(strings.Fields(html.UnescapeString(m[2])), " ")
		}
		r.Items = append(r.Items, it)
	}
	return r, true
}

func (r CMSReport) headline() string {
	if len(r.Items) == 0 {
		return ""
	}
	if a := r.Items[0].Advisory; a != "" {
		return a
	}
	return r.Items[0].Component
}

func (r CMSReport) summary() string {
	s := "A380 maintenance system report"
	if h := r.headline(); h != "" {
		s += ": " + h
		if n := len(r.Items); n > 1 {
			s += fmt.Sprintf(" and %d more", n-1)
		}
	}
	var who []string
	if r.Callsign != "" {
		who = append(who, r.Callsign)
	}
	if r.Origin != "" && r.Dest != "" {
		who = append(who, r.Origin+"→"+r.Dest)
	}
	if len(who) > 0 {
		s += " (" + strings.Join(who, " ") + ")"
	}
	return s
}

func (r CMSReport) lines() string {
	var l []string
	for _, it := range r.Items {
		s := it.Component
		if it.Advisory != "" {
			s = it.Advisory + " (" + it.Component + ")"
		}
		var more []string
		if it.Class != "" {
			more = append(more, strings.ToLower(it.Class))
		}
		if it.System != "" {
			more = append(more, "system "+it.System)
		}
		if !it.At.IsZero() {
			more = append(more, it.At.Format("2006-01-02 15:04:05")+"Z")
		}
		if it.Code != "" {
			more = append(more, "code "+it.Code)
		}
		l = append(l, s+" ["+strings.Join(more, ", ")+"]")
	}
	return strings.Join(l, "\n")
}

func decodeCMS(m Message) (Result, bool) {
	rep, ok := ParseCMS(m.Text)
	if !ok {
		return Result{}, false
	}
	r := Result{Kind: KindMaint, Label: m.Label, Dir: m.Dir, Body: strings.TrimSpace(m.Text), Fields: map[string]any{"cms": rep}}
	if r.Dir == Unknown {
		r.Dir = Downlink
	}
	r.Identity.Tail, r.Identity.Callsign = rep.Tail, rep.Callsign
	r.Summary, r.Text = rep.summary(), rep.lines()
	return r, true
}
