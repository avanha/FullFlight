// Package datalink fetches airframes.io messages for watched aircraft, decodes
// them ourselves, and stores the result. airframes.io's derived fields are kept
// as hints and compared against our own decode.
package datalink

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/decoder"
	"github.com/avanha/fullflight/internal/geo"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/waypoints"
)

// FixWindow is how far from a message position we look for another fix to
// check it against.
const FixWindow = 30 * time.Minute

var iataFlight = regexp.MustCompile(`^([A-Z0-9]{2})0*(\d{1,4})[A-Z]?$`)

// normFlight turns "CX0833" or "cx833" into "CX833"; ok is false for anything
// that is not a 2-letter-designator flight number (e.g. ICAO "CPA833").
func normFlight(s string) (string, bool) {
	m := iataFlight.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return "", false
	}
	return m[1] + m[2], true
}

// Ingest decodes one airframes.io message fetched for hexAddr and stores it.
// It reports whether a new row was written; messages without text, and
// repeats of a message already held, return false.
func Ingest(st *store.Store, hexAddr string, raw airframes.Message) (bool, error) {
	text := string(raw.Text)
	if text == "" {
		return false, nil
	}
	m := store.Message{
		ID: raw.ID, Hex: hexAddr, T: raw.Timestamp.UTC(), Label: string(raw.Label), BlockID: string(raw.BlockID),
		MsgNum: string(raw.MessageNumber), SourceType: string(raw.SourceType), Station: string(raw.Station.Ident),
		Freq: raw.Frequency, Text: text,
		FromHex: strings.ToUpper(string(raw.FromHex)), ToHex: strings.ToUpper(string(raw.ToHex)),
		AFDir:    strings.ToLower(string(raw.LinkDirection)),
		AFTail:   firstNonEmpty(string(raw.Tail), string(raw.Airframe.Tail)),
		AFFlight: firstNonEmpty(string(raw.FlightNumber), string(raw.Flight.Flight)),
		AFDep:    string(raw.Departing), AFDest: string(raw.Destination),
	}
	dir := directionOf(m.AFDir, m.FromHex, m.ToHex, hexAddr)

	decodeInto(st, &m, dir)
	return st.AddMessage(m)
}

// decodeInto fills m's derived fields (everything from our own decode of the
// raw text, plus the checks against airframes.io's hints and other fixes).
func decodeInto(st *store.Store, m *store.Message, dir decoder.Direction) {
	m.Kind, m.Direction, m.Tail, m.Callsign, m.Flight = "", "", "", "", ""
	m.Summary, m.Detail, m.Decoded, m.Route, m.Suspect = "", "", "", "", ""
	m.Lat, m.Lon, m.AltFt, m.PosT = nil, nil, nil, nil
	m.WxDir, m.WxKt, m.WxTemp, m.GS, m.Trk, m.Fixes = nil, nil, nil, nil, nil, ""

	res, err := decoder.Decode(decoder.Message{Label: m.Label, Dir: dir, Text: m.Text})
	if err != nil {
		// Keep the raw message even if decoding fails.
		m.Kind, m.Summary = string(decoder.KindUnknown), "decode error: "+err.Error()
	} else {
		m.Kind, m.Direction = string(res.Kind), map[decoder.Direction]string{decoder.Uplink: "up", decoder.Downlink: "down"}[res.Dir]
		m.Tail, m.Callsign, m.Flight = res.Identity.Tail, res.Identity.Callsign, res.Identity.Flight
		m.Summary, m.Detail = res.Summary, res.Text
		b, _ := json.Marshal(map[string]any{"tree": res.Tree, "fields": res.Fields, "identity": res.Identity, "prefix": res.Prefix})
		m.Decoded = string(b)
		if len(res.Route) > 0 {
			b, _ := json.Marshal(res.Route)
			m.Route = string(b)
		}
		if res.Pos != nil {
			t := res.Pos.At(m.T)
			m.Lat, m.Lon, m.AltFt, m.PosT = &res.Pos.Lat, &res.Pos.Lon, res.Pos.AltFt, &t
			if res.Kind == decoder.KindFMS {
				m.TimeNote, m.TimeFlag = compareTimes(m.T, fmt.Sprint(res.Fields["wpt_time"]))
			}
			// A position whose time is in doubt cannot be judged against where other fixes were at that
			// time: it may well be a true position at some other time. It is marked unverified instead.
			if !m.TimeFlag {
				m.Suspect = checkPosition(st, m.Hex, m.ID, res.Pos.Lat, res.Pos.Lon, t)
			}
		}
		if res.Wx != nil {
			m.WxDir, m.WxKt, m.WxTemp = res.Wx.WindDirDeg, res.Wx.WindKt, res.Wx.TempC
		}
		m.GS, m.Trk = res.GS, res.Track
		m.Fixes = fixList(res, m)
	}
	m.Mismatch = mismatch(st, m.Hex, *m)
}

// TimeTolerance is how far a message's own time may differ from its arrival
// time before the placement is called unverified. The two agree within a few
// minutes for healthy stations; some are hours apart for unknown reasons.
const TimeTolerance = 10 * time.Minute

// compareTimes describes a message's own hhmmss time against when it arrived,
// and reports whether they disagree by more than TimeTolerance. The embedded
// time has no date, so the difference is taken modulo a day, in (-12h, 12h].
func compareTimes(arrived time.Time, hhmmss string) (note string, flag bool) {
	if len(hhmmss) != 6 {
		return "", false
	}
	h, e1 := strconv.Atoi(hhmmss[:2])
	m, e2 := strconv.Atoi(hhmmss[2:4])
	s, e3 := strconv.Atoi(hhmmss[4:])
	if e1 != nil || e2 != nil || e3 != nil || h > 23 || m > 59 || s > 59 {
		return "", false
	}
	a := arrived.UTC()
	d := time.Duration(h*3600+m*60+s)*time.Second - time.Duration(a.Hour()*3600+a.Minute()*60+a.Second())*time.Second
	for d > 12*time.Hour {
		d -= 24 * time.Hour
	}
	for d <= -12*time.Hour {
		d += 24 * time.Hour
	}
	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}
	flag = d > TimeTolerance
	note = fmt.Sprintf("arrived %s Z, report waypoint time %s:%s:%s (%s%s)", a.Format("15:04:05"), hhmmss[:2], hhmmss[2:4], hhmmss[4:], sign, d.Round(time.Second))
	if flag {
		note += ": more than 10 minutes apart, so the position's time is unverified"
	}
	return note, flag
}

// fixList records the waypoints a CPDLC position report names, with their times placed on the
// right day (the date nearest the report: an ETA can be just after midnight), and the report's
// own speed and track for projecting the next one.
func fixList(res decoder.Result, m *store.Message) string {
	if len(res.Fixes) == 0 {
		return ""
	}
	ref := m.T
	if m.PosT != nil {
		ref = *m.PosT
	}
	fl := store.FixList{GS: res.GS, Trk: res.Track}
	for _, f := range res.Fixes {
		e := store.FixEntry{Name: f.Name, Role: f.Role, AltFt: f.AltFt}
		if f.Secs != nil {
			e.T = decoder.NearestTimeOfDay(*f.Secs, ref).Unix()
		}
		fl.List = append(fl.List, e)
	}
	b, _ := json.Marshal(fl)
	return string(b)
}

// Redecode re-runs our decoder over every stored message, oldest first, and
// rewrites the derived columns. Run it after improving the decoder so old
// messages pick up the change. It returns how many messages it changed.
func Redecode(st *store.Store) (changed int, err error) {
	all, err := st.AllMessages()
	if err != nil {
		return 0, err
	}
	for _, old := range all {
		m := old
		// Not the stored direction: that may itself have been a guess.
		decodeInto(st, &m, directionOf(m.AFDir, m.FromHex, m.ToHex, m.Hex))
		if m.Kind == old.Kind && m.Summary == old.Summary && m.Decoded == old.Decoded && m.Route == old.Route &&
			m.Suspect == old.Suspect && m.Mismatch == old.Mismatch && sameFloat(m.Lat, old.Lat) && sameFloat(m.Lon, old.Lon) &&
			m.Fixes == old.Fixes && sameFloat(m.WxKt, old.WxKt) && sameFloat(m.WxTemp, old.WxTemp) && sameFloat(m.GS, old.GS) && sameFloat(m.Trk, old.Trk) {
			continue
		}
		if err := st.UpdateDecoded(m); err != nil {
			return changed, err
		}
		changed++
	}
	// With everything re-decoded, settle the suspect flags against full neighbourhoods.
	hexes := map[string]bool{}
	for _, m := range all {
		hexes[m.Hex] = true
	}
	for h := range hexes {
		n, err := Recheck(st, h, time.Time{})
		if err != nil {
			return changed, err
		}
		changed += n
		if _, err := waypoints.Learn(st, h, time.Time{}); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func sameFloat(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// directionOf says whether a message went up to or down from the aircraft.
// airframes.io's own direction field is usually empty, so fall back on which
// end of the link is the aircraft; Unknown if neither says.
func directionOf(afDir, from, to, hexAddr string) decoder.Direction {
	switch afDir {
	case "uplink":
		return decoder.Uplink
	case "downlink":
		return decoder.Downlink
	}
	switch {
	case from != "" && strings.EqualFold(from, hexAddr):
		return decoder.Downlink
	case to != "" && strings.EqualFold(to, hexAddr):
		return decoder.Uplink
	}
	return decoder.Unknown
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// NeighbourFixes is how many fixes each side of a position it is judged against.
const NeighbourFixes = 2

// checkPosition returns a reason if the reported position cannot be right
// given the fixes around it in time. A position is only suspect if it is
// impossible against every one of them: a coherent run of fixes vouch for one
// another, so one conflicting cluster does not condemn another, a single
// outlier does not condemn its good neighbours, and the verdict does not
// depend on which was stored first.
func checkPosition(st *store.Store, hexAddr string, id int64, lat, lon float64, t time.Time) string {
	fixes := st.Neighbors(hexAddr, t, FixWindow, id, NeighbourFixes)
	var why []string
	for i := range fixes {
		if w := impossible(lat, lon, t, &fixes[i]); w != "" {
			why = append(why, w)
		}
	}
	if len(fixes) == 0 || len(why) < len(fixes) {
		return ""
	}
	if len(why) > 2 {
		why = why[:2]
	}
	return strings.Join(why, "; ")
}

// impossible explains why an aircraft cannot be at (lat, lon) at t given
// fix f, or returns "".
func impossible(lat, lon float64, t time.Time, f *store.Fix) string {
	dt := f.T.Sub(t).Abs()
	dist := geo.DistNM(lat, lon, f.Lat, f.Lon)
	if dt < 30*time.Second {
		if dist > 20 {
			return fmt.Sprintf("%.0f nm from the %s fix at the same time", dist, f.From)
		}
		return ""
	}
	if kt := dist / dt.Hours(); kt > decoder.MaxPlausibleKt {
		return fmt.Sprintf("implies %.0f kt against the %s fix %s away", kt, f.From, dt.Round(time.Second))
	}
	return ""
}

// Recheck re-evaluates the suspect flag of every positioned message for an
// aircraft since the given time, now that more of its neighbours are stored.
// It returns how many flags changed.
func Recheck(st *store.Store, hexAddr string, since time.Time) (changed int, err error) {
	ms, err := st.Messages(hexAddr, since, time.Now().Add(48*time.Hour), 100000)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.PosT == nil || m.Lat == nil || m.Lon == nil {
			continue
		}
		why := ""
		if !m.TimeFlag {
			why = checkPosition(st, hexAddr, m.ID, *m.Lat, *m.Lon, *m.PosT)
		}
		if why != m.Suspect {
			if err := st.SetSuspect(m.ID, why); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

// mismatch lists where airframes.io's idea of the message disagrees with our
// decode of the raw text.
func mismatch(st *store.Store, hexAddr string, m store.Message) string {
	var out []string
	if m.Tail != "" && m.AFTail != "" && !strings.EqualFold(m.Tail, m.AFTail) {
		out = append(out, fmt.Sprintf("tail: airframes.io says %s, text says %s", m.AFTail, m.Tail))
	}
	if ours, ok1 := normFlight(m.Flight); ok1 {
		if theirs, ok2 := normFlight(m.AFFlight); ok2 && ours != theirs {
			out = append(out, fmt.Sprintf("flight: airframes.io says %s, text says %s", theirs, ours))
		}
	}
	if m.Tail != "" {
		if h, ok := st.HexForTail(strings.ToUpper(m.Tail)); ok && !strings.EqualFold(h, hexAddr) {
			out = append(out, fmt.Sprintf("text names %s, which is hex %s, not %s", m.Tail, h, hexAddr))
		}
	}
	return strings.Join(out, "; ")
}
