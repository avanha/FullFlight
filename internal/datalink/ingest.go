// Package datalink fetches airframes.io messages for watched aircraft, decodes
// them ourselves, and stores the result. airframes.io's derived fields are kept
// as hints and compared against our own decode.
package datalink

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/decoder"
	"github.com/avanha/fullflight/internal/geo"
	"github.com/avanha/fullflight/internal/store"
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
	dir := decoder.Unknown
	switch strings.ToLower(string(raw.LinkDirection)) {
	case "uplink":
		dir = decoder.Uplink
	case "downlink":
		dir = decoder.Downlink
	}

	m := store.Message{
		ID: raw.ID, Hex: hexAddr, T: raw.Timestamp.UTC(), Label: string(raw.Label), BlockID: string(raw.BlockID),
		MsgNum: string(raw.MessageNumber), SourceType: string(raw.SourceType), Station: string(raw.Station.Ident),
		Freq: raw.Frequency, Text: text,
		AFTail:   firstNonEmpty(string(raw.Tail), string(raw.Airframe.Tail)),
		AFFlight: firstNonEmpty(string(raw.FlightNumber), string(raw.Flight.Flight)),
		AFDep:    string(raw.Departing), AFDest: string(raw.Destination),
	}

	decodeInto(st, &m, dir)
	return st.AddMessage(m)
}

// decodeInto fills m's derived fields (everything from our own decode of the
// raw text, plus the checks against airframes.io's hints and other fixes).
func decodeInto(st *store.Store, m *store.Message, dir decoder.Direction) {
	m.Kind, m.Direction, m.Tail, m.Callsign, m.Flight = "", "", "", "", ""
	m.Summary, m.Detail, m.Decoded, m.Route, m.Suspect = "", "", "", "", ""
	m.Lat, m.Lon, m.AltFt, m.PosT = nil, nil, nil, nil

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
			m.Suspect = checkPosition(st, m.Hex, m.ID, res.Pos.Lat, res.Pos.Lon, t)
		}
	}
	m.Mismatch = mismatch(st, m.Hex, *m)
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
		dir := decoder.Unknown
		switch m.Direction {
		case "up":
			dir = decoder.Uplink
		case "down":
			dir = decoder.Downlink
		}
		decodeInto(st, &m, dir)
		if m.Kind == old.Kind && m.Summary == old.Summary && m.Decoded == old.Decoded && m.Route == old.Route &&
			m.Suspect == old.Suspect && m.Mismatch == old.Mismatch && sameFloat(m.Lat, old.Lat) && sameFloat(m.Lon, old.Lon) {
			continue
		}
		if err := st.UpdateDecoded(m); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func sameFloat(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// checkPosition returns a reason if the reported position cannot be right
// given the nearest other fix of the same aircraft.
func checkPosition(st *store.Store, hexAddr string, id int64, lat, lon float64, t time.Time) string {
	fix, ok := st.NearestFix(hexAddr, t, FixWindow, id)
	if !ok {
		return ""
	}
	dt := fix.T.Sub(t).Abs()
	dist := geo.DistNM(lat, lon, fix.Lat, fix.Lon)
	if dt < 30*time.Second {
		if dist > 20 {
			return fmt.Sprintf("%.0f nm from the %s fix at the same time", dist, fix.From)
		}
		return ""
	}
	if kt := dist / dt.Hours(); kt > decoder.MaxPlausibleKt {
		return fmt.Sprintf("implies %.0f kt against the %s fix %s away", kt, fix.From, dt.Round(time.Second))
	}
	return ""
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
