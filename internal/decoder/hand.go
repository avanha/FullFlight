package decoder

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ARINC 702 FMS position report: POS<N|S><lat><E|W><lon>,<wpt>,<time>,<FL>,<next>,<eta>,<next+1>,...
	posnRe  = regexp.MustCompile(`^POS([NS])(\d{2})(\d{3})([EW])(\d{3})(\d{3})(,|$)`)
	afnRe   = regexp.MustCompile(`^/([A-Z0-9]{7})\.AFN/(.*)$`)
	datisRe = regexp.MustCompile(`^/([A-Z0-9]{4,7})\.TI2/\d{3}([A-Z0-9]{4})([A-Z])`)
	mediaRe = regexp.MustCompile(`^0([EL])([A-Z0-9])(\d{6})([A-Z0-9]*)/?$`)
	rep081  = regexp.MustCompile(`REP081,\d+,\d+;[A-Z0-9]+/NX,([A-Z]{4}) ([A-Z]{4})/0,7,([+-]\d+),([+-]\d+),(\d+),(\d+),([+-]\d+),(\d+),(\d+)`)
	counter = regexp.MustCompile(`\|A350,(\d+),`)
)

var linkNames = map[byte]string{
	'V': "VHF ACARS", 'S': "satcom", 'H': "HF", 'G': "gatelink",
	'C': "cellular", '2': "VDL2", 'I': "Iridium",
}

// parseAFN handles the AFN segments we have seen: FMH (flight/tail/time) and
// FCA (contact advisory naming the next ground station). libacars does not
// decode AFN here.
func parseAFN(body string, r *Result) {
	m := afnRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	r.Fields["gs_addr"] = m[1]
	var other []string
	for _, seg := range strings.Split(m[2], "/") {
		switch {
		case strings.HasPrefix(seg, "FMH"):
			f := strings.Split(seg[3:], ",")
			r.Identity.Callsign = f[0]
			if len(f) > 1 {
				r.Identity.Tail = strings.TrimLeft(f[1], ".")
			}
			if len(f) > 3 && len(f[3]) == 6 {
				r.Fields["time_hhmmss"] = f[3]
			}
		case strings.HasPrefix(seg, "FCA") && len(seg) >= 10:
			r.Fields["afn"] = "contact_advisory"
			r.Fields["contact_station"] = seg[3:10]
		case seg != "":
			other = append(other, seg)
		}
	}
	if len(other) > 0 {
		r.Fields["unparsed_segments"] = other
	}
}

// parseDATIS decodes a D-ATIS request: airport and A(rrival)/D(eparture).
func parseDATIS(body string, r *Result) {
	m := datisRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	r.Fields["gs_addr"] = m[1]
	r.Fields["airport"] = m[2]
	switch m[3] {
	case "A":
		r.Fields["atis_type"] = "arrival"
	case "D":
		r.Fields["atis_type"] = "departure"
	default:
		r.Fields["atis_type"] = m[3]
	}
}

// parseMedia decodes a media advisory such as "0ES124056VS": E(stablished) or
// L(ost), link, hhmmss UTC, then the list of links available.
func parseMedia(body string, r *Result) {
	m := mediaRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	r.Fields["state"] = map[string]string{"E": "established", "L": "lost"}[m[1]]
	r.Fields["link"] = linkName(m[2][0])
	r.Fields["time_hhmmss"] = m[3]
	var avail []string
	for i := 0; i < len(m[4]); i++ {
		avail = append(avail, linkName(m[4][i]))
	}
	r.Fields["available"] = avail
}

func linkName(c byte) string {
	if n, ok := linkNames[c]; ok {
		return n
	}
	return string(c)
}

// REP081Report is the partially decoded Cathay A350 MIAM report.
type REP081Report struct {
	Origin, Dest string
	Counter      string
	Lat, Lon     float64
	SecsInMonth  int64 // seconds since 00:00 UTC on the 1st; see ResolveMonthSeconds
	AltFt        int
	TempC        float64
	WindDirDeg   int
	WindKt       int
}

// ParseREP081 extracts the validated fields of a REP081 report from raw text.
func ParseREP081(text string) (REP081Report, bool) {
	m := rep081.FindStringSubmatch(text)
	if m == nil {
		return REP081Report{}, false
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	secs, _ := strconv.ParseInt(m[5], 10, 64)
	rep := REP081Report{
		Origin: m[1], Dest: m[2],
		Lat:         float64(atoi(m[3])) / 3600,
		Lon:         float64(atoi(m[4])) / 3600,
		SecsInMonth: secs,
		AltFt:       atoi(m[6]) * 10,
		TempC:       float64(atoi(m[7])) / 10,
		WindDirDeg:  atoi(m[8]),
		WindKt:      atoi(m[9]),
	}
	if c := counter.FindStringSubmatch(text); c != nil {
		rep.Counter = c[1]
	}
	return rep, true
}

// ResolveMonthSeconds turns "seconds since the 1st of the month, 00:00 UTC"
// into a time, using ref (the receive time) to pick the month: the latest
// candidate not more than an hour after ref.
func ResolveMonthSeconds(secs int64, ref time.Time) time.Time {
	ref = ref.UTC()
	first := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, time.UTC)
	t := first.Add(time.Duration(secs) * time.Second)
	if t.After(ref.Add(time.Hour)) {
		first = first.AddDate(0, -1, 0)
		t = first.Add(time.Duration(secs) * time.Second)
	}
	return t
}

// parseFMS decodes a "POS" report from the flight management system: position
// as degrees and tenths of minutes, then flight level and the waypoints. The
// time fields in these reports do not match UTC in any sample we have seen, so
// the report is placed at its receipt time and the embedded times are ignored.
func parseFMS(body string, r *Result) {
	m := posnRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	atof := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	lat := atof(m[2]) + atof(m[3])/10/60
	lon := atof(m[5]) + atof(m[6])/10/60
	if m[1] == "S" {
		lat = -lat
	}
	if m[4] == "W" {
		lon = -lon
	}
	p := &Pos{Lat: lat, Lon: lon, Basis: BasisReceipt}
	f := strings.Split(strings.TrimSpace(body), ",")
	get := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	if fl, err := strconv.Atoi(get(3)); err == nil && fl > 0 && fl < 700 {
		ft := float64(fl) * 100
		p.AltFt = &ft
	}
	r.Pos = p
	if t := get(2); len(t) == 6 {
		r.Fields["wpt_time"] = t // hhmmss, as sent
	}
	for k, i := range map[string]int{"waypoint": 1, "next_waypoint": 4, "next_next_waypoint": 6} {
		if v := get(i); v != "" {
			r.Fields[k] = v
		}
	}
}
