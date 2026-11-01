package decoder

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// How a Pos time is expressed.
const (
	BasisHour    = "hour"    // seconds past the hour (ADS-C)
	BasisMonth   = "month"   // seconds since 00:00 UTC on the 1st (REP081)
	BasisDay     = "day"     // seconds since 00:00 UTC (CPDLC position report)
	BasisDayHint = "dayhint" // time of day and day of the month (POSRPT): Secs is day*100000 + seconds of day
	BasisDate    = "date"    // month, day, hour and minute (*POS report): Secs is the number MMDDHHMM
	BasisWindow  = "window"  // between two times of day (POSA1): Secs is first*100000 + last, in seconds of day
	BasisReceipt = "receipt" // no usable embedded time: placed at when it was received
)

// Pos is a position reported inside a message, with its partial timestamp.
type Pos struct {
	Lat, Lon float64
	AltFt    *float64
	Secs     float64
	Basis    string
}

// At places the position in time, using recv (when the message was received)
// to supply the missing hour or month.
func (p Pos) At(recv time.Time) time.Time {
	switch p.Basis {
	case BasisReceipt:
		return recv.UTC()
	case BasisMonth:
		return ResolveMonthSeconds(int64(p.Secs), recv)
	case BasisDay:
		return ResolveSecondsOfDay(p.Secs, recv)
	case BasisDate:
		return ResolveMonthDay(int(p.Secs), recv)
	case BasisWindow:
		return windowTime(p.Secs, recv)
	case BasisDayHint:
		return ResolveDayTime(int(p.Secs)/100000, float64(int(p.Secs)%100000), recv)
	}
	return ResolveSecondsPastHour(p.Secs, recv)
}

// Waypoint is a predicted ADS-C route point; ETASec is relative to the report.
type Waypoint struct {
	Lat, Lon float64
	AltFt    *float64
	ETASec   *float64
	// Kind is "next" or "next+1" for the predicted route, "fixed" for the flight computer's
	// projection of where it will be after a fixed time. Empty in rows stored before this existed.
	Kind string
}

func num(m map[string]any, k string) *float64 {
	if v, ok := m[k].(float64); ok {
		return &v
	}
	return nil
}

// adscPosition pulls the first position-carrying tag (periodic report,
// waypoint change, ...) and the predicted route out of an ADS-C tree.
func adscPosition(tree map[string]any) (*Pos, []Waypoint) {
	arinc, _ := tree["arinc622"].(map[string]any)
	adsc, _ := arinc["adsc"].(map[string]any)
	tags, _ := adsc["tags"].([]any)
	var pos *Pos
	var route []Waypoint
	var fixedProj *Waypoint
	for _, t := range tags {
		tm, _ := t.(map[string]any)
		for name, v := range tm {
			body, _ := v.(map[string]any)
			if body == nil {
				continue
			}
			if name == "predicted_route" {
				for _, k := range []struct{ key, kind string }{{"next_wpt", "next"}, {"next_next_wpt", "next+1"}} {
					if w, _ := body[k.key].(map[string]any); w != nil && num(w, "lat") != nil && num(w, "lon") != nil {
						route = append(route, Waypoint{Lat: *num(w, "lat"), Lon: *num(w, "lon"), AltFt: num(w, "alt"), ETASec: num(w, "eta_sec"), Kind: k.kind})
					}
				}
				continue
			}
			if name == "fixed_projection" && num(body, "lat") != nil && num(body, "lon") != nil {
				fixedProj = &Waypoint{Lat: *num(body, "lat"), Lon: *num(body, "lon"), AltFt: num(body, "alt"), ETASec: num(body, "eta_sec"), Kind: "fixed"}
				continue
			}
			if pos == nil && num(body, "lat") != nil && num(body, "lon") != nil && num(body, "ts_sec") != nil {
				pos = &Pos{Lat: *num(body, "lat"), Lon: *num(body, "lon"), AltFt: num(body, "alt"), Secs: *num(body, "ts_sec"), Basis: BasisHour}
			}
		}
	}
	if fixedProj != nil && len(route) > 0 { // the order matters to readers: next, next+1, then the projection
		route = append(route, *fixedProj)
	}
	return pos, route
}

// cpdlcFirstLabel returns the label of the first message element of a CPDLC message: the one in the
// message itself, ahead of the further elements a message may carry in a list after it.
func cpdlcFirstLabel(tree map[string]any) (string, bool) {
	arinc, _ := tree["arinc622"].(map[string]any)
	cpdlc, _ := arinc["cpdlc"].(map[string]any)
	for _, dir := range []string{"atc_uplink_msg", "atc_downlink_msg"} {
		msg, _ := cpdlc[dir].(map[string]any)
		el, _ := msg[dir+"_element_id"].(map[string]any)
		if l, ok := el["choice_label"].(string); ok {
			return l, true
		}
	}
	return findString(tree, "choice_label") // some other shape: at least the same answer every time
}

// signedDegrees reads libacars' {"deg":19,"min":10.3,"dir":"north"} form.
func signedDegrees(m map[string]any, neg string) (float64, bool) {
	deg, min := num(m, "deg"), num(m, "min")
	dir, _ := m["dir"].(string)
	if deg == nil || min == nil || dir == "" {
		return 0, false
	}
	v := *deg + *min/60
	if dir == neg {
		v = -v
	}
	return v, true
}

func sub(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m, _ = m[k].(map[string]any)
		if m == nil {
			return nil
		}
	}
	return m
}

func clock(m map[string]any) (float64, bool) {
	h, mi := num(m, "hour"), num(m, "min")
	if h == nil || mi == nil {
		return 0, false
	}
	s := 0.0
	if x := num(m, "sec"); x != nil {
		s = *x
	}
	return *h*3600 + *mi*60 + s, true
}

// cpdlcPosition extracts the position from a FANS-1/A downlink position
// report. Reports that name a fix instead of giving coordinates have no Pos
// (we carry no navigation database); the fix name is returned for display.
func cpdlcPosition(tree map[string]any) (pos *Pos, fix string) {
	el := sub(tree, "arinc622", "cpdlc", "atc_downlink_msg")
	rep := sub(el, "atc_downlink_msg_element_id", "data", "pos_report")
	if rep == nil {
		return nil, ""
	}
	cur := sub(rep, "pos_current")
	if f, _ := sub(cur, "data")["fix"].(string); f != "" {
		fix = f
	}
	ll := sub(cur, "data", "lat_lon")
	lat, ok1 := signedDegrees(sub(ll, "lat"), "south")
	lon, ok2 := signedDegrees(sub(ll, "lon"), "west")
	if !ok1 || !ok2 {
		return nil, fix
	}
	// Time at the position; fall back to the message header's time.
	secs, ok := clock(sub(rep, "time_at_pos_current"))
	if !ok {
		if secs, ok = clock(sub(el, "header", "timestamp")); !ok {
			return nil, fix
		}
	}
	p := &Pos{Lat: lat, Lon: lon, Secs: secs, Basis: BasisDay}
	if fl := num(sub(rep, "alt", "data"), "flight_level"); fl != nil {
		ft := *fl * 100
		p.AltFt = &ft
	}
	return p, fix
}

func hhmmss(s string) string {
	if len(s) != 6 {
		return s
	}
	return s[:2] + ":" + s[2:4] + ":" + s[4:] + "Z"
}

func latlon(lat, lon float64) string {
	ns, ew := "N", "E"
	if lat < 0 {
		ns, lat = "S", -lat
	}
	if lon < 0 {
		ew, lon = "W", -lon
	}
	return fmt.Sprintf("%.3f%s %.3f%s", lat, ns, lon, ew)
}

func summarize(r Result) string {
	switch r.Kind {
	case KindADSC:
		if r.Pos == nil {
			return "ADS-C message"
		}
		s := "ADS-C report " + latlon(r.Pos.Lat, r.Pos.Lon)
		if r.Pos.AltFt != nil {
			s += fmt.Sprintf(" %.0f ft", *r.Pos.AltFt)
		}
		if len(r.Route) > 0 {
			s += fmt.Sprintf(" → next %s", latlon(r.Route[0].Lat, r.Route[0].Lon))
		}
		return s
	case KindCPDLC:
		if r.Pos != nil {
			s := "CPDLC position report " + latlon(r.Pos.Lat, r.Pos.Lon)
			if r.Pos.AltFt != nil {
				s += fmt.Sprintf(" %.0f ft", *r.Pos.AltFt)
			}
			return s + fixNames(r.Fixes)
		}
		if fix, ok := r.Fields["fix"].(string); ok {
			return "CPDLC position report at " + fix + fixNames(r.Fixes)
		}
		if l, ok := cpdlcFirstLabel(r.Tree); ok {
			return "CPDLC " + map[Direction]string{Uplink: "uplink", Downlink: "downlink"}[r.Dir] + ": " + l
		}
		return "CPDLC message"
	case KindMIAM:
		if rep, ok := r.Fields["rep081"].(REP081Report); ok {
			s := fmt.Sprintf("REP081 %s→%s %s FL%03d, OAT %.1f°C, wind %03d°/%dkt",
				rep.Origin, rep.Dest, latlon(rep.Lat, rep.Lon), rep.AltFt/100, rep.TempC, rep.WindDirDeg, rep.WindKt)
			if n := len(rep.Samples); n > 1 {
				s += fmt.Sprintf(" (newest of %d fixes over %d s)", n, rep.Samples[n-1].SecsInMonth-rep.Samples[0].SecsInMonth)
			}
			return s
		}
		if a, ok := r.Fields["a350"].(A350Report); ok {
			return a.summary()
		}
		if x, ok := r.Fields["rtr"].(RTRReport); ok {
			return x.summary()
		}
		if x, ok := r.Fields["takeoff"].(TakeoffReport); ok {
			return x.summary()
		}
		if x, ok := r.Fields["departure"].(DepartureReport); ok {
			return x.summary()
		}
		if strings.Contains(r.Text, "MIAM CORE Ack") {
			if n, ok := findNum(r.Tree, "msg_ack_num"); ok {
				return fmt.Sprintf("MIAM acknowledgement of message %.0f", n)
			}
			return "MIAM acknowledgement"
		}
		if t, ok := findString(r.Tree, "text"); ok {
			if len(t) > 70 {
				t = t[:70] + "…"
			}
			return "MIAM: " + t
		}
		if !miamFrame.MatchString(r.Body) {
			// no frame header: a later block of a message too long for one, whose first block holds the header
			return SummaryMIAMBlock
		}
		return SummaryMIAMUndecoded
	case KindAFN:
		parts := []string{"AFN"}
		if r.Fields["afn"] == "contact_advisory" {
			parts = []string{fmt.Sprintf("AFN contact advisory: log on to %v", r.Fields["contact_station"])}
		}
		if r.Fields["afn"] == "acknowledgement" {
			s := "AFN logon acknowledged"
			if f, ok := r.Fields["facility"].(string); ok {
				s += " by " + f
			}
			if apps, _ := r.Fields["apps"].([]AFNApp); len(apps) > 0 {
				var names []string
				for _, a := range apps {
					names = append(names, a.Label())
				}
				s += ": " + strings.Join(names, ", ")
			}
			parts = []string{s}
		}
		if r.Identity.Callsign != "" {
			parts = append(parts, r.Identity.Callsign)
		}
		return strings.Join(parts, ", ")
	case KindMedia:
		if st, ok := r.Fields["state"].(string); ok {
			return fmt.Sprintf("Link %s: %v at %s (available: %s)", st, r.Fields["link"], hhmmss(fmt.Sprint(r.Fields["time_hhmmss"])),
				strings.Join(r.Fields["available"].([]string), ", "))
		}
		return "Media advisory"
	case KindDATIS:
		return fmt.Sprintf("D-ATIS %v request for %v", r.Fields["atis_type"], r.Fields["airport"])
	case KindWeather:
		if w, ok := r.Fields["weather"].(WeatherReport); ok {
			return w.atisSummary()
		}
		return "Weather report"
	case KindDisconnect:
		s := "Data link disconnect"
		if r.Identity.Tail != "" {
			s += ": " + r.Identity.Tail
		}
		return s
	case KindACMF:
		return acmfSummary(r)
	case KindAOC:
		return aocSummary(r)
	case KindDeparture:
		if d, ok := r.Fields["departure"].(DepartureReport); ok {
			return d.summary()
		}
		if n, ok := r.Fields["departure"].(UPSNotice); ok {
			return n.summary()
		}
		return undecodedSummary(r)
	case KindPOS:
		return posRptSummary(r)
	case KindPOSRPT:
		return posrptSummary(r)
	case KindPOSA1:
		return posa1Summary(r)
	case KindSBPOS:
		return sbPosSummary(r)
	case KindFMS:
		if r.Pos == nil {
			return "FMS position report"
		}
		s := "FMS position report " + latlon(r.Pos.Lat, r.Pos.Lon)
		if r.Pos.AltFt != nil {
			s += fmt.Sprintf(" FL%03.0f", *r.Pos.AltFt/100)
		}
		if n, ok := r.Fields["next_waypoint"]; ok {
			s += fmt.Sprintf(", next %v", n)
		}
		return s
	}
	return undecodedSummary(r)
}

// undecodedSummary describes a message we have no decoder for, such as the
// airline-specific engine and performance reports sent as label H1.
func undecodedSummary(r Result) string {
	snippet := strings.Join(strings.Fields(r.Body), " ")
	if len(snippet) > 44 {
		snippet = snippet[:44] + "…"
	}
	label := r.Label
	if label == "" {
		label = "?"
	}
	return fmt.Sprintf("Undecoded %s message: %s", label, snippet)
}

// What a MIAM message we can say nothing about is called; the page lists these with the undecoded messages.
const (
	SummaryMIAMUndecoded = "MIAM (not decoded)"
	SummaryMIAMBlock     = "MIAM data block (part of a longer message)"
)

var miamFrame = regexp.MustCompile(`^[TFKSAXY][-.0-3][0-3]`)

// Weather is the wind (direction it blows FROM, true) and temperature a report carries.
type Weather struct {
	WindDirDeg, WindKt, TempC *float64
}

func (w *Weather) empty() bool {
	return w == nil || (w.WindDirDeg == nil && w.WindKt == nil && w.TempC == nil)
}

// FixRef is a waypoint a CPDLC position report names. Role is "passed" (with the time it was
// passed), "next" (with its ETA) or "then" (the one after, with no time). Secs is the time of day.
type FixRef struct {
	Name, Role string
	Secs       *float64
	AltFt      *float64
}

// adscMotionWeather reads ground speed, true track and the meteo group from an ADS-C tree.
func adscMotionWeather(tree map[string]any) (gs, trk *float64, wx *Weather) {
	arinc, _ := tree["arinc622"].(map[string]any)
	adsc, _ := arinc["adsc"].(map[string]any)
	tags, _ := adsc["tags"].([]any)
	for _, t := range tags {
		tm, _ := t.(map[string]any)
		if e, _ := tm["earth_ref_data"].(map[string]any); e != nil {
			gs = num(e, "gnd_spd_kts")
			if v, ok := e["true_trk_valid"].(bool); ok && !v {
				continue
			}
			trk = num(e, "true_trk_deg")
		}
		if m, _ := tm["meteo_data"].(map[string]any); m != nil {
			w := &Weather{WindKt: num(m, "wind_spd_kts"), TempC: num(m, "temp_c")}
			if v, ok := m["wind_dir_valid"].(bool); !ok || v {
				w.WindDirDeg = num(m, "wind_dir_true_deg")
			}
			if !w.empty() {
				wx = w
			}
		}
	}
	return
}

// cpdlcExtras reads ground speed, track, wind, temperature and the named waypoints from a
// FANS-1/A downlink position report.
func cpdlcExtras(tree map[string]any) (gs, trk *float64, wx *Weather, fixes []FixRef) {
	rep := sub(tree, "arinc622", "cpdlc", "atc_downlink_msg", "atc_downlink_msg_element_id", "data", "pos_report")
	if rep == nil {
		return
	}
	if v := sub(rep, "speed_gnd"); v != nil && v["unit"] == "kts" {
		gs = num(v, "val")
	}
	if c, _ := sub(rep, "trk_angle")["choice"].(string); c == "degreesTrue" {
		trk = num(sub(rep, "trk_angle", "data", "deg_true"), "val")
	}
	w := &Weather{}
	if wind := sub(rep, "winds"); wind != nil {
		w.WindDirDeg = num(sub(wind, "wind_dir"), "val")
		w.WindKt = num(sub(wind, "wind_speed", "data", "wind_speed_english"), "val")
	}
	w.TempC = num(sub(rep, "temp", "data", "temp_deg_c"), "val")
	if !w.empty() {
		wx = w
	}
	name := func(key string) string { s, _ := sub(rep, key, "data")["fix"].(string); return s }
	if n := name("reported_wpt_pos"); n != "" {
		f := FixRef{Name: n, Role: "passed"}
		if secs, ok := clock(sub(rep, "reported_wpt_time")); ok {
			f.Secs = &secs
		}
		if fl := num(sub(rep, "reported_wpt_alt", "data"), "flight_level"); fl != nil {
			ft := *fl * 100
			f.AltFt = &ft
		}
		fixes = append(fixes, f)
	}
	if n := name("next_fix"); n != "" {
		f := FixRef{Name: n, Role: "next"}
		if secs, ok := clock(sub(rep, "eta_at_fix_next")); ok {
			f.Secs = &secs
		}
		fixes = append(fixes, f)
	}
	if n := name("next_next_fix"); n != "" {
		fixes = append(fixes, FixRef{Name: n, Role: "then"})
	}
	return
}

// fixNames renders the waypoints a position report named: " — passed OLGAN 18:26Z, next KADAB 18:55Z, then KAPPU".
func fixNames(fixes []FixRef) string {
	var parts []string
	for _, f := range fixes {
		p := f.Role + " " + f.Name
		if f.Secs != nil {
			p += fmt.Sprintf(" %02d:%02dZ", int(*f.Secs)/3600%24, int(*f.Secs)/60%60)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return ""
	}
	return " \u2014 " + strings.Join(parts, ", ")
}
