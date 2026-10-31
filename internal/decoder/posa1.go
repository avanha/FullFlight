package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// China Cargo's position report, label 16 (seen from B-221W and B-223A, over VDL2 and satellite):
//
//	POSA1N59929E149881,DERUD  ,011141,360,OVGAR  ,013635,-50, 46, 391,823
//
// latitude and longitude in thousandths of a degree (the longitude is padded with spaces: E  9100 is 9.100),
// the waypoint last passed and the time it was passed (HHMMSS UTC), flight level, the next waypoint and its
// ETA, temperature, wind speed in knots, and two numbers we cannot explain. Over the ocean the waypoints can be
// bare coordinates (N41E160); on the ground there are none. Read from samples, not a specification.
//
// The time is that of the waypoint, not of the position, which lies between it and the next one: a report that
// arrived inside that window is taken to be from when it arrived, one that arrived outside it (delivered late)
// from the middle of it. A satellite message has a number and "ACK" in front: M84ACK0624POSA1...
var posa1Re = regexp.MustCompile(`^POSA1([NS])(\d{5})([EW])([ \d]{6}),([^,]*),(\d{6}| *),([ \d]{1,3}),([^,]*),(\d{6}|-{6}| *),\s*(-?\d+),\s*(\d+|-+),\s*(\d+|-+),\s*(\d+|-+)\s*$`)

func parsePOSA1(body string, r *Result) {
	m := posa1Re.FindStringSubmatch(strings.TrimSpace(body))
	if m == nil {
		return
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(strings.TrimSpace(s)); return v }
	lat := float64(atoi(m[2])) / 1000
	lon := float64(atoi(m[4])) / 1000
	if lat > 90 || lon > 180 {
		return
	}
	if m[1] == "S" {
		lat = -lat
	}
	if m[3] == "W" {
		lon = -lon
	}
	alt := float64(atoi(m[7])) * 100
	secs := func(s string) (int, bool) {
		if len(s) != 6 || s[0] == '-' {
			return 0, false
		}
		h, mi, se := atoi(s[:2]), atoi(s[2:4]), atoi(s[4:])
		if h > 23 || mi > 59 || se > 59 {
			return 0, false
		}
		return h*3600 + mi*60 + se, true
	}
	r.Pos = &Pos{Lat: lat, Lon: lon, AltFt: &alt, Basis: BasisReceipt}
	t1, ok1 := secs(m[6])
	t2, ok2 := secs(m[9])
	switch {
	case ok1 && ok2:
		r.Pos.Basis, r.Pos.Secs = BasisWindow, float64(t1*100000+t2)
	case ok1:
		r.Pos.Basis, r.Pos.Secs = BasisDay, float64(t1)
	}
	if w := strings.TrimSpace(m[5]); w != "" {
		r.Fields["last_wpt"] = w
	}
	if w := strings.TrimSpace(m[8]); w != "" && w != "-" {
		r.Fields["next_wpt"] = w
	}
	temp := float64(atoi(m[10]))
	r.Wx = &Weather{TempC: &temp}
	if !strings.HasPrefix(m[11], "-") {
		r.Fields["wind_kt"] = atoi(m[11]) // direction not reported
	}
	r.Fields["values"] = []string{m[12], m[13]} // unexplained; dashes on the ground
}

// windowTime places a BasisWindow position: the first time on or before recv (allowing ClockSkew), the last one
// after it, and recv itself if it falls between them, else the middle.
func windowTime(secs float64, recv time.Time) time.Time {
	recv = recv.UTC()
	s1, s2 := int(secs)/100000, int(secs)%100000
	first := ResolveSecondsOfDay(float64(s1), recv)
	last := first.Add(time.Duration((s2-s1+86400)%86400) * time.Second)
	if !recv.Before(first) && !recv.After(last) {
		return recv
	}
	return first.Add(last.Sub(first) / 2)
}

func posa1Summary(r Result) string {
	if r.Pos == nil {
		return "Position report"
	}
	s := "Position report " + latlon(r.Pos.Lat, r.Pos.Lon)
	if r.Pos.AltFt != nil {
		s += fmt.Sprintf(" FL%03.0f", *r.Pos.AltFt/100)
	}
	if w, _ := r.Fields["next_wpt"].(string); w != "" {
		s += ", next " + w
	}
	return s
}
