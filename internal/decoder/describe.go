package decoder

import (
	"fmt"
	"regexp"
	"strings"
)

// A message we cannot decode is still described as well as its form allows: a family of maintenance data is named as one,
// readable text is shown as text (and listed by default), and anything else says how much of what there is.
//
// The families are recognised by the way they start, from samples; what is inside them is not known.
var dataFamilies = []struct {
	re   *regexp.Regexp
	name string // may use $1
}{
	{regexp.MustCompile(`^RPT(\d\d);PG\d;REAL`), "Real-time system report RPT$1"},
	{regexp.MustCompile(`^CFG\d+ `), "Aircraft configuration report"},
	{regexp.MustCompile(`^A380\d{6},\d,\d,\w+;REP(\d+)`), "A380 maintenance report REP$1"},
	{regexp.MustCompile(`^A380\d{6},`), "A380 maintenance data"},
	{regexp.MustCompile(`^\.[A-Z0-9]{7} \d{6}\s+(?:DFD|CFD)\b`), "Ground request for aircraft data"},
	{regexp.MustCompile(`^/BLVBOCR\.`), "Aircraft system report"},
	{regexp.MustCompile(`^(?:[A-Z0-9]{10}#M1B)?(?:RES(?:REQ)?)?/(?:AK|AC|RJ),`), "Acknowledgement"},
	{regexp.MustCompile(`^<CMS>`), "Maintenance system report"},
	{regexp.MustCompile(`^-?/*\s*(PNT|TXN|ATC|WTL|LDF|LDP) [A-Z]{2}\d\d`), "Air Canada $1 message"},
	{regexp.MustCompile(`^C\d\d[A-Z]{3}\s+\d[A-Z]{5}`), "Air Canada aircraft event message"},
	{regexp.MustCompile(`^AG(?:SND|IMC)`), "Air Canada message"},
	{regexp.MustCompile(`^ACM\d\d`), "Aircraft condition monitoring report"},
	{regexp.MustCompile(`^/WX\d\d[A-Z]{2}\d\d`), "Wind and weather report"},
	{regexp.MustCompile(`^\d{3}SC\d+[A-Z]{2}\d+N\d+[A-Z]{2}`), "747 data report"},
	{regexp.MustCompile(`#DFB.*PNT\*\*|^PNT\*\*\d{4}`), "Position and progress report"},
	{regexp.MustCompile(`^\d{2,3}N\d{3}DA\d{6}`), "Delta aircraft data report"},
	{regexp.MustCompile(`#DFBEB\d{4}A[AG]_`), "Aircraft performance data"},
	{regexp.MustCompile(`HWPN|HWSN|\bLRU (?:Left|Right)\b|\bSW\d+ \d+ [A-Z]{2,5} (?:OPS|DB|AMI|ASO)`), "Software and hardware status table"},
	{regexp.MustCompile(`^/ILNGE7X\.`), "787 engine report"},
	{regexp.MustCompile(`^<\d{3}>[A-Z]{3}`), "787 engine report"},
	{regexp.MustCompile(`^EB\d{4}A[AG]_`), "Aircraft performance data"},
	{regexp.MustCompile(`^-?FSQ [A-Z0-9]+\s*$`), "Air Canada company message FSQ (no text)"},
	{regexp.MustCompile(`^[A-Z]{3}\d\d-\w{3,4}-\d{4} SW\d+|^\s*(?:[A-Z]{3,4}\d\d-\w{3,4}-\d{4}\s+SW\d+ \d+ [A-Z]{3,5} [A-Z ]+)+$`), "Software and database status table"},
	{regexp.MustCompile(`HIS504B0304`), "Compressed data file"},
	{regexp.MustCompile(`^<[^>]{0,8}>$|^\W{20,}$|^[.\s]{20,}$`), "Padding"},
}

// text kinds: what readable text is about, by its content.
var textTopics = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`SIGMET`), "SIGMET"},
	{regexp.MustCompile(`\bTAF(?:OR)?\b`), "Forecast (TAF)"},
	{regexp.MustCompile(`\bMETAR\b`), "METAR"},
	{regexp.MustCompile(`LOADSHEET`), "Load sheet"},
	{regexp.MustCompile(`PDC UPLINK|PDC REQUEST`), "Pre-departure clearance"},
	{regexp.MustCompile(`\bATIS\b`), "ATIS text"},
	{regexp.MustCompile(`(?i)\bNOTAM\b`), "NOTAM"},
	{regexp.MustCompile(`CRNCE|CLRNCE|CLEARANCE`), "Clearance"},
	{regexp.MustCompile(`TURBULENCE|TURBC`), "Turbulence advisory"},
	{regexp.MustCompile(`PLND PUSH|PUSH UPDATE`), "Push update"},
}

// A short progress report: the flight (FN) and the destination with the runway it expects (DT), in either order.
var (
	progFlightRe = regexp.MustCompile(`^PRG/.*?\bFN([A-Z]{2,3}\d+)`)
	progDestRe   = regexp.MustCompile(`/DT([A-Z]{4}),(\d{2}[LRC]?)`)
)

var (
	wordRe = regexp.MustCompile(`[A-Za-z]{3,}`)
	lineRe = regexp.MustCompile(`[\r\n]+`)
)

// readable reports whether text is words, not a table or an encoded block: several words of letters make up a good share of it.
func readable(text string) bool {
	words := wordRe.FindAllString(text, -1)
	if len(words) < 3 {
		return false
	}
	letters, all := 0, 0
	for _, w := range words {
		letters += len(w)
	}
	for _, c := range text {
		if c != ' ' && c != '\r' && c != '\n' && c != '\t' {
			all++
		}
	}
	return all > 0 && float64(letters)/float64(all) > 0.45
}

// oneLine joins the lines of a message with " / " and cuts it at n characters.
func oneLine(body string, n int) string {
	var lines []string
	for _, l := range lineRe.Split(body, -1) {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	s := strings.Join(lines, " / ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// describeUnknown says what a message nothing decoded is. Readable text becomes a text message (listed by default);
// the rest stays "unknown" with a description.
func describeUnknown(r *Result) {
	body := strings.TrimSpace(r.Body)
	flat := strings.Join(strings.Fields(body), " ")
	// the text as sent, before anything was taken for a message number: PRG/ and RESREQ/ look like one
	full := strings.Join(strings.Fields(r.Prefix+r.Body), " ")
	label := r.Label
	if label == "" {
		label = "?"
	}
	for _, text := range []string{flat, full} {
		for _, f := range dataFamilies {
			if m := f.re.FindStringSubmatchIndex(text); m != nil {
				name := string(f.re.ExpandString(nil, f.name, text, m))
				if name == "Padding" {
					r.Summary = fmt.Sprintf("Blank or padding data (label %s)", label)
				} else {
					r.Summary = fmt.Sprintf("%s (label %s, not decoded)", name, label)
				}
				return
			}
		}
		if f := progFlightRe.FindStringSubmatch(text); f != nil {
			if d := progDestRe.FindStringSubmatch(text); d != nil {
				r.Summary = fmt.Sprintf("Progress report %s: to %s, runway %s (label %s, not decoded)", f[1], d[1], d[2], label)
				return
			}
		}
	}
	topic := ""
	for _, t := range textTopics {
		if t.re.MatchString(flat) {
			topic = t.name
			break
		}
	}
	if numeric(flat) {
		r.Summary = fmt.Sprintf("Numeric data block (label %s, %d characters, not decoded)", label, len(flat))
		return
	}
	if len(flat) >= 20 && (topic != "" || readable(body)) {
		r.Kind = KindText
		who := "Message"
		switch {
		case label == "7B":
			who = "Crew message"
		case r.Dir == Downlink:
			who = "Message from the aircraft"
		case r.Dir == Uplink:
			who = "Message to the aircraft"
		}
		if topic != "" {
			who = topic
		}
		r.Summary = who + ": " + oneLine(body, 110)
		r.Text = strings.Join(strings.Split(strings.ReplaceAll(body, "\r", ""), "\n"), "\n")
		return
	}
	r.Summary = fmt.Sprintf("Data message (label %s, %d characters, not decoded)", label, len(flat))
}

// numeric reports whether a message is almost all digits and signs: the continuation of a table of numbers.
func numeric(s string) bool {
	n, digits := 0, 0
	for _, c := range s {
		if c == ' ' {
			continue
		}
		n++
		if c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' {
			digits++
		}
	}
	return n >= 12 && float64(digits)/float64(n) > 0.85
}
