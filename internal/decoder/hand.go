package decoder

import (
	"regexp"
	"sort"
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
	// REP081 has a header naming the route and then one sample per minute or so: the older form carries one (A06/NX), the
	// newer one several (EDR06/03/NX), each as index, flag, lat, lon (arc-seconds), seconds in the month, altitude/10, OAT/10, wind.
	rep081Head   = regexp.MustCompile(`(?s)REP081,\d+,\d+;(?:[A-Z]\d+|EDR\d+/\d+)/NX,([A-Z]{4}) ([A-Z]{4})/(.*)`)
	rep081Sample = regexp.MustCompile(`^\d+,\d+,([+-]\d{1,7}),([+-]\d{1,7}),(\d+),(\d+),([+-]\d+),(\d+),(\d+)`)
	counter      = regexp.MustCompile(`A350,(\d+),`)
)

var linkNames = map[byte]string{
	'V': "VHF ACARS", 'S': "satcom", 'H': "HF", 'G': "gatelink",
	'C': "cellular", '2': "VDL2", 'I': "Iridium",
}

// AFNApp is one application a ground station announced in an AFN logon response.
type AFNApp struct {
	Name    string `json:"name"` // ADS or ATC, as sent
	Version string `json:"version,omitempty"`
	Address string `json:"address,omitempty"` // the ground end of the link, when given
}

// Label is the application's usual name: ATC is CPDLC.
func (a AFNApp) Label() string {
	switch a.Name {
	case "ADS":
		return "ADS-C"
	case "ATC":
		return "CPDLC"
	}
	return a.Name
}

// parseAFN handles the AFN segments we have seen: FMH (flight/tail/time) and
// FCA (contact advisory naming the next ground station), FAK (the logon
// acknowledgement, with the facility it answers for) and FAR (the applications
// the ground station will use, each with a version and sometimes an address).
// libacars does not decode AFN here.
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
		case strings.HasPrefix(seg, "FAK"):
			f := strings.Split(seg[3:], ",")
			r.Fields["afn"] = "acknowledgement"
			if len(f) > 1 && f[1] != "" {
				r.Fields["facility"] = f[1]
			}
		case strings.HasPrefix(seg, "FAR") && len(seg) > 3:
			f := strings.Split(seg[3:], ",")
			a := AFNApp{Name: f[0]}
			if len(f) > 1 {
				a.Version = f[1]
			}
			if len(f) > 2 {
				a.Address = f[2]
			}
			apps, _ := r.Fields["apps"].([]AFNApp)
			r.Fields["apps"] = append(apps, a)
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
	if w, ok := readATIS(body); ok { // the broadcast itself, sent to the aircraft
		r.Kind = KindWeather
		r.Fields["weather"] = w
		r.Fields["airport"], r.Fields["atis_type"] = w.Airport, w.Type
		return
	}
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

// REP081Sample is one position in a REP081 report.
type REP081Sample struct {
	Lat, Lon    float64
	SecsInMonth int64 // seconds since 00:00 UTC on the 1st; see ResolveMonthSeconds
	AltFt       int
	TempC       float64
	WindDirDeg  int
	WindKt      int
}

// REP081Report is the partially decoded Cathay A350 MIAM report. Its position, time, altitude and weather are those of
// the newest sample; a report from the newer software carries several, oldest first in Samples.
type REP081Report struct {
	Origin, Dest string
	Counter      string
	Lat, Lon     float64
	SecsInMonth  int64
	AltFt        int
	TempC        float64
	WindDirDeg   int
	WindKt       int
	Samples      []REP081Sample
}

// ParseREP081 extracts the validated fields of a REP081 report from text: the raw message, or what libacars made of
// a compressed one.
func ParseREP081(text string) (REP081Report, bool) {
	m := rep081Head.FindStringSubmatch(text)
	if m == nil {
		return REP081Report{}, false
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	var samples []REP081Sample
	for _, part := range strings.Split(m[3], "/") {
		g := rep081Sample.FindStringSubmatch(part)
		if g == nil {
			continue
		}
		secs, _ := strconv.ParseInt(g[3], 10, 64)
		sm := REP081Sample{
			Lat: float64(atoi(g[1])) / 3600, Lon: float64(atoi(g[2])) / 3600, SecsInMonth: secs,
			AltFt: atoi(g[4]) * 10, TempC: float64(atoi(g[5])) / 10, WindDirDeg: atoi(g[6]), WindKt: atoi(g[7]),
		}
		if sm.Lat < -90 || sm.Lat > 90 || sm.Lon < -180 || sm.Lon > 180 {
			continue
		}
		samples = append(samples, sm)
	}
	if len(samples) == 0 {
		return REP081Report{}, false
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].SecsInMonth < samples[j].SecsInMonth })
	n := samples[len(samples)-1]
	rep := REP081Report{
		Origin: m[1], Dest: m[2], Lat: n.Lat, Lon: n.Lon, SecsInMonth: n.SecsInMonth,
		AltFt: n.AltFt, TempC: n.TempC, WindDirDeg: n.WindDirDeg, WindKt: n.WindKt, Samples: samples,
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
	r.Wx = fmsWeather(get(7), get(8))
	if t := get(2); len(t) == 6 {
		r.Fields["wpt_time"] = t // hhmmss, as sent
	}
	for k, i := range map[string]int{"waypoint": 1, "next_waypoint": 4, "next_next_waypoint": 6} {
		if v := get(i); v != "" {
			r.Fields[k] = v
		}
	}
	// The waypoints it names, as a CPDLC report's are, so the map can place them by name: the one passed (with its time), the next
	// (with its ETA) and the one after. The times of day are as sent; the map judges them against where the aircraft was.
	for _, f := range []struct {
		role       string
		name, time int
	}{{"passed", 1, 2}, {"next", 4, 5}, {"then", 6, -1}} {
		if get(f.name) == "" {
			continue
		}
		ref := FixRef{Name: get(f.name), Role: f.role}
		if f.time >= 0 {
			ref.Secs = timeOfDay(get(f.time))
		}
		r.Fixes = append(r.Fixes, ref)
	}
}

// timeOfDay reads "153019" as seconds into the day, or nil if it is not a time.
func timeOfDay(s string) *float64 {
	if len(s) != 6 {
		return nil
	}
	h, e1 := strconv.Atoi(s[:2])
	m, e2 := strconv.Atoi(s[2:4])
	sec, e3 := strconv.Atoi(s[4:])
	if e1 != nil || e2 != nil || e3 != nil || h > 23 || m > 59 || sec > 59 {
		return nil
	}
	v := float64(h*3600 + m*60 + sec)
	return &v
}

// fmsWeather reads the temperature ("M51" = -51 C, "P05" = +5 C) and wind ("264080" = from 264
// degrees at 80 knots) groups of an FMS position report. Values outside what is physically
// plausible are dropped rather than shown.
func fmsWeather(temp, wind string) *Weather {
	w := &Weather{}
	if len(temp) >= 2 && (temp[0] == 'M' || temp[0] == 'P') {
		if v, err := strconv.Atoi(temp[1:]); err == nil && v <= 90 {
			c := float64(v)
			if temp[0] == 'M' {
				c = -c
			}
			w.TempC = &c
		}
	}
	if len(wind) == 6 {
		d, e1 := strconv.Atoi(wind[:3])
		k, e2 := strconv.Atoi(wind[3:])
		if e1 == nil && e2 == nil && d <= 360 && k <= 250 {
			dd, kk := float64(d), float64(k)
			w.WindDirDeg, w.WindKt = &dd, &kk
		}
	}
	if w.empty() {
		return nil
	}
	return w
}
