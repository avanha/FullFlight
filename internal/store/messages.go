package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/geo"
)

// DupWindow is how close in time two receptions of the same text must be to
// count as one message heard by several stations.
const DupWindow = 10 * time.Minute

// Message is one stored datalink message: the raw fields as received, our
// decode of the text, and airframes.io's own opinions (af_*) kept separate.
type Message struct {
	ID         int64
	Hex        string
	T          time.Time
	Label      string
	BlockID    string
	MsgNum     string
	Direction  string
	SourceType string
	Station    string
	Freq       *float64
	Text       string
	RxCount    int

	Kind, Tail, Callsign, Flight string
	Summary, Detail              string
	Decoded, Route               string // JSON
	Lat, Lon, AltFt              *float64
	PosT                         *time.Time
	Suspect                      string

	AFTail, AFFlight, AFDep, AFDest string
	Mismatch                        string

	// Raw link-layer facts used to work out the direction.
	FromHex, ToHex, AFDir string

	// TimeNote compares the message's own time with its arrival time;
	// TimeFlag is set when they disagree by more than TimeTolerance.
	TimeNote string
	TimeFlag bool

	// Weather at the report, and the speed and track it gave.
	WxDir, WxKt, WxTemp *float64
	GS, Trk             *float64
	// Fixes is a JSON FixList: the waypoints a CPDLC position report names.
	Fixes string
}

// FixEntry is one waypoint named by a CPDLC position report. T is the unix time it was passed
// (role "passed") or is expected (role "next"); 0 for "then", which carries none.
type FixEntry struct {
	Name  string   `json:"name"`
	Role  string   `json:"role"`
	T     int64    `json:"t,omitempty"`
	AltFt *float64 `json:"alt,omitempty"`
}

// FixList is what Message.Fixes holds. GS and Trk (knots, degrees true) are the report's own
// speed and track, used to project the next fix when its coordinates are not known.
type FixList struct {
	GS   *float64   `json:"gs,omitempty"`
	Trk  *float64   `json:"trk,omitempty"`
	List []FixEntry `json:"list"`
}

// DupKey identifies a message independent of which station heard it.
func DupKey(hexAddr, label, text string) string {
	h := sha256.Sum256([]byte(hexAddr + "|" + label + "|" + text))
	return hex.EncodeToString(h[:8])
}

// AddMessage stores m unless it is already stored. A reception of text we
// already hold (same aircraft, label and text within DupWindow) only bumps
// rx_count. It reports whether a new row was written.
func (s *Store) AddMessage(m Message) (bool, error) {
	key := DupKey(m.Hex, m.Label, m.Text)
	var id int64
	err := s.db.QueryRow(`SELECT id FROM message WHERE dup_key=? AND ts BETWEEN ? AND ? LIMIT 1`,
		key, ts(m.T.Add(-DupWindow)), ts(m.T.Add(DupWindow))).Scan(&id)
	if err == nil {
		if id == m.ID {
			return false, nil // the same row again
		}
		_, err = s.db.Exec(`UPDATE message SET rx_count = rx_count + 1 WHERE id=?`, id)
		return false, err
	}
	var posT *int64
	if m.PosT != nil {
		v := ts(*m.PosT)
		posT = &v
	}
	cols := []struct {
		name string
		val  any
	}{
		{"id", m.ID}, {"hex", m.Hex}, {"ts", ts(m.T)}, {"label", m.Label}, {"block_id", m.BlockID}, {"msg_num", m.MsgNum},
		{"direction", m.Direction}, {"source_type", m.SourceType}, {"station", m.Station}, {"freq", m.Freq},
		{"text", m.Text}, {"dup_key", key}, {"kind", m.Kind}, {"tail", m.Tail}, {"callsign", m.Callsign}, {"flight", m.Flight},
		{"summary", m.Summary}, {"detail", m.Detail}, {"decoded", m.Decoded}, {"route", m.Route},
		{"lat", m.Lat}, {"lon", m.Lon}, {"alt_ft", m.AltFt}, {"pos_ts", posT}, {"suspect", m.Suspect},
		{"af_tail", m.AFTail}, {"af_flight", m.AFFlight}, {"af_dep", m.AFDep}, {"af_dest", m.AFDest}, {"mismatch", m.Mismatch},
		{"from_hex", m.FromHex}, {"to_hex", m.ToHex}, {"af_dir", m.AFDir}, {"time_note", m.TimeNote}, {"time_flag", boolInt(m.TimeFlag)},
		{"wx_dir", m.WxDir}, {"wx_kt", m.WxKt}, {"wx_temp", m.WxTemp}, {"gs", m.GS}, {"trk", m.Trk}, {"fixes", m.Fixes},
	}
	names, marks := make([]string, len(cols)), make([]string, len(cols))
	vals := make([]any, len(cols))
	for i, c := range cols {
		names[i], marks[i], vals[i] = c.name, "?", c.val
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO message (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(marks, ",")+`)`, vals...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const messageCols = `id, hex, ts, label, block_id, msg_num, direction, source_type, station, freq, text, rx_count,
	kind, tail, callsign, flight, summary, detail, decoded, route, lat, lon, alt_ft, pos_ts, suspect,
	af_tail, af_flight, af_dep, af_dest, mismatch, from_hex, to_hex, af_dir, time_note, time_flag,
	wx_dir, wx_kt, wx_temp, gs, trk, fixes`

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var t int64
		var posT *int64
		var tflag int
		if err := rows.Scan(&m.ID, &m.Hex, &t, &m.Label, &m.BlockID, &m.MsgNum, &m.Direction, &m.SourceType, &m.Station, &m.Freq,
			&m.Text, &m.RxCount, &m.Kind, &m.Tail, &m.Callsign, &m.Flight, &m.Summary, &m.Detail, &m.Decoded, &m.Route,
			&m.Lat, &m.Lon, &m.AltFt, &posT, &m.Suspect, &m.AFTail, &m.AFFlight, &m.AFDep, &m.AFDest, &m.Mismatch, &m.FromHex, &m.ToHex, &m.AFDir, &m.TimeNote, &tflag,
			&m.WxDir, &m.WxKt, &m.WxTemp, &m.GS, &m.Trk, &m.Fixes); err != nil {
			return nil, err
		}
		m.T, m.TimeFlag = time.Unix(t, 0).UTC(), tflag == 1
		if posT != nil {
			v := time.Unix(*posT, 0).UTC()
			m.PosT = &v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Messages returns an aircraft's messages in [since, until), oldest first,
// limited to the most recent limit rows.
func (s *Store) Messages(hexAddr string, since, until time.Time, limit int) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM message WHERE hex=? AND ts>=? AND ts<?
		ORDER BY ts DESC, id DESC LIMIT ?`, hexAddr, ts(since), ts(until), limit)
	if err != nil {
		return nil, err
	}
	out, err := scanMessages(rows)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // oldest first
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// AllMessages returns every stored message, oldest first.
func (s *Store) AllMessages() ([]Message, error) {
	rows, err := s.db.Query(`SELECT ` + messageCols + ` FROM message ORDER BY ts, id`)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// UpdateDecoded rewrites the columns derived from the raw text, leaving the
// raw fields and airframes.io hints alone.
func (s *Store) UpdateDecoded(m Message) error {
	var posT *int64
	if m.PosT != nil {
		v := ts(*m.PosT)
		posT = &v
	}
	_, err := s.db.Exec(`UPDATE message SET direction=?, kind=?, tail=?, callsign=?, flight=?, summary=?, detail=?, decoded=?, route=?,
		lat=?, lon=?, alt_ft=?, pos_ts=?, suspect=?, mismatch=?, time_note=?, time_flag=?,
		wx_dir=?, wx_kt=?, wx_temp=?, gs=?, trk=?, fixes=? WHERE id=?`,
		m.Direction, m.Kind, m.Tail, m.Callsign, m.Flight, m.Summary, m.Detail, m.Decoded, m.Route,
		m.Lat, m.Lon, m.AltFt, posT, m.Suspect, m.Mismatch, m.TimeNote, boolInt(m.TimeFlag),
		m.WxDir, m.WxKt, m.WxTemp, m.GS, m.Trk, m.Fixes, m.ID)
	return err
}

// FetchedTo returns the end of the last successful fetch for an aircraft.
func (s *Store) FetchedTo(hexAddr string) (time.Time, bool) {
	var v int64
	if err := s.db.QueryRow(`SELECT fetched_to FROM fetch_state WHERE hex=?`, hexAddr).Scan(&v); err != nil {
		return time.Time{}, false
	}
	return time.Unix(v, 0).UTC(), true
}

func (s *Store) SetFetchedTo(hexAddr string, t time.Time) error {
	_, err := s.db.Exec(`INSERT INTO fetch_state (hex, fetched_to) VALUES (?,?)
		ON CONFLICT (hex) DO UPDATE SET fetched_to=excluded.fetched_to`, hexAddr, ts(t))
	return err
}

// Fix is a known position of an aircraft, from ADS-B or a message.
type Fix struct {
	Lat, Lon float64
	T        time.Time
	From     string // "adsb" or "message"
}

// Neighbors returns up to k fixes before and k after t (each within window)
// from ADS-B and from messages, nearest first on each side, ignoring message
// excludeID. Message fixes count whether or not they are flagged suspect, so
// that a coherent run of fixes can vouch for one another. Messages whose time is unverified
// are left out: they say nothing about where the aircraft was at the time they are filed under.
func (s *Store) Neighbors(hexAddr string, t time.Time, window time.Duration, excludeID int64, k int) []Fix {
	lo, hi, at := ts(t.Add(-window)), ts(t.Add(window)), ts(t)
	collect := func(q, from string, args ...any) []Fix {
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []Fix
		for rows.Next() {
			var f Fix
			var u int64
			if rows.Scan(&f.Lat, &f.Lon, &u) == nil {
				f.T, f.From = time.Unix(u, 0).UTC(), from
				out = append(out, f)
			}
		}
		return out
	}
	nearest := func(fs []Fix) []Fix {
		sort.Slice(fs, func(i, j int) bool { return fs[i].T.Sub(t).Abs() < fs[j].T.Sub(t).Abs() })
		if len(fs) > k {
			fs = fs[:k]
		}
		return fs
	}
	const adsb = `SELECT lat, lon, ts FROM position WHERE hex=? AND ts BETWEEN ? AND ? ORDER BY ts %s LIMIT ?`
	const msg = `SELECT lat, lon, pos_ts FROM message WHERE hex=? AND pos_ts BETWEEN ? AND ? AND lat IS NOT NULL AND time_flag=0 AND id<>? ORDER BY pos_ts %s LIMIT ?`
	before := append(collect(fmt.Sprintf(adsb, "DESC"), "adsb", hexAddr, lo, at, k),
		collect(fmt.Sprintf(msg, "DESC"), "message", hexAddr, lo, at, excludeID, k)...)
	after := append(collect(fmt.Sprintf(adsb, "ASC"), "adsb", hexAddr, at, hi, k),
		collect(fmt.Sprintf(msg, "ASC"), "message", hexAddr, at, hi, excludeID, k)...)
	return append(nearest(before), nearest(after)...)
}

// SetSuspect records (or clears, with "") why a message's position looks wrong.
func (s *Store) SetSuspect(id int64, why string) error {
	_, err := s.db.Exec(`UPDATE message SET suspect=? WHERE id=?`, why, id)
	return err
}

// MessagePosition is the newest position reported inside a message.
type MessagePosition struct {
	T        time.Time
	Kind     string // adsc, cpdlc, miam (REP081) or fms_position
	Lat, Lon float64
	AltFt    *float64
}

// LastMessagePosition returns the newest position carried by a message for the
// aircraft, skipping ones flagged suspect or whose time is unverified.
func (s *Store) LastMessagePosition(hexAddr string) (MessagePosition, bool) {
	var p MessagePosition
	var t int64
	err := s.db.QueryRow(`SELECT pos_ts, kind, lat, lon, alt_ft FROM message
		WHERE hex=? AND pos_ts IS NOT NULL AND lat IS NOT NULL AND suspect='' AND time_flag=0
		ORDER BY pos_ts DESC LIMIT 1`, hexAddr).Scan(&t, &p.Kind, &p.Lat, &p.Lon, &p.AltFt)
	if err != nil {
		return MessagePosition{}, false
	}
	p.T = time.Unix(t, 0).UTC()
	return p, true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Route is an origin/destination pair an aircraft reported about itself.
type Route struct {
	From, To string // ICAO codes
	T        time.Time
	Source   string // "REP081", "A350 report", "ACMF", "airline report" or "flight plan"
}

// LastRoute returns the newest origin/destination reported by the aircraft in a
// message that names one, with the time of that report.
func (s *Store) LastRoute(hexAddr string) (Route, bool) {
	var r Route
	var t int64
	err := s.db.QueryRow(`SELECT t, origin, dest, src FROM (`+routeReportsSQL+`) ORDER BY t DESC LIMIT 1`, hexAddr, 0, 1<<40).Scan(&t, &r.From, &r.To, &r.Source)
	if err != nil {
		return Route{}, false
	}
	r.T = time.Unix(t, 0).UTC()
	return r, true
}

// FlightID is a flight an aircraft's messages name, as we decoded it from the raw text.
type FlightID struct {
	Flight   string // IATA-style flight number from the message prefix, e.g. CX113
	Callsign string // ICAO callsign from an AFN logon, e.g. CPA113
	T        time.Time
}

// LastFlight returns the newest message that names the flight the aircraft is operating.
// Messages whose text names a different aircraft than the one asked for are skipped:
// their flight belongs to that other aircraft.
func (s *Store) LastFlight(hexAddr string) (FlightID, bool) {
	var f FlightID
	var t int64
	err := s.db.QueryRow(`SELECT ts, flight, callsign FROM message
		WHERE hex=? AND (flight != '' OR callsign != '') AND mismatch NOT LIKE '%which is hex%' AND time_flag=0
		ORDER BY ts DESC, id DESC LIMIT 1`, hexAddr).Scan(&t, &f.Flight, &f.Callsign)
	if err != nil {
		return FlightID{}, false
	}
	f.T = time.Unix(t, 0).UTC()
	return f, true
}

// LearnedFix is a waypoint position matched from the aircraft's own data, with how many messages
// agreed on it.
type LearnedFix struct {
	Lat, Lon float64
	N        int
}

// AddFix records that message msgID, which named waypoint name, matches a waypoint at (lat, lon).
// Recording the same evidence again changes nothing.
func (s *Store) AddFix(name string, msgID int64, lat, lon float64, source string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO fix (name, msg_id, lat, lon, source) VALUES (?,?,?,?,?)`, name, msgID, lat, lon, source)
	return err
}

// fixRow is one piece of evidence: a message that named a waypoint, and where the match put it.
type fixRow struct {
	name     string
	lat, lon float64
	source   string
	ts       sql.NullInt64 // when the message was received, if it is still stored
}

// clusterFixes groups evidence into places: for each name, positions within a quarter of a degree are
// one place. The place is the position most of its evidence agrees on (ADS-C coordinates repeat
// exactly), not their mean, which a single mismatched report would drag off. Clusters come in the
// order they were first met.
func clusterFixes(rows []fixRow) []FixSummary {
	type acc struct {
		FixSummary
		lat0, lon0 float64
		votes      map[[2]int64]int // exact positions (to 1e-5 degree) and how many reports gave each
		best       int
	}
	var all []*acc
	byName := map[string][]*acc{}
	for _, r := range rows {
		var a *acc
		for _, c := range byName[r.name] {
			if math.Abs(c.lat0-r.lat) < 0.25 && math.Abs(geo.DLon(c.lon0, r.lon)) < 0.25 {
				a = c
				break
			}
		}
		if a == nil {
			a = &acc{FixSummary: FixSummary{Name: r.name, Lat: r.lat, Lon: r.lon}, lat0: r.lat, lon0: r.lon, votes: map[[2]int64]int{}}
			byName[r.name] = append(byName[r.name], a)
			all = append(all, a)
		}
		a.N++
		k := [2]int64{int64(math.Round(r.lat * 1e5)), int64(math.Round(geo.NormLon(r.lon) * 1e5))}
		if a.votes[k]++; a.votes[k] > a.best {
			a.best, a.Lat, a.Lon = a.votes[k], r.lat, r.lon
		}
		if r.ts.Valid {
			t := time.Unix(r.ts.Int64, 0).UTC()
			if a.First.IsZero() || t.Before(a.First) {
				a.First = t
			}
			if t.After(a.Last) {
				a.Last = t
			}
		}
		if !slices.Contains(a.Sources, r.source) {
			a.Sources = append(a.Sources, r.source)
		}
	}
	out := make([]FixSummary, len(all))
	for i, a := range all {
		out[i] = a.FixSummary
	}
	return out
}

// LearnedFixes returns the positions learned for a waypoint name, most supported first.
func (s *Store) LearnedFixes(name string) []LearnedFix {
	rows, err := s.db.Query(`SELECT lat, lon FROM fix WHERE name=? ORDER BY msg_id`, name)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ev []fixRow
	for rows.Next() {
		r := fixRow{name: name}
		if rows.Scan(&r.lat, &r.lon) == nil {
			ev = append(ev, r)
		}
	}
	var out []LearnedFix
	for _, c := range clusterFixes(ev) {
		out = append(out, LearnedFix{c.Lat, c.Lon, c.N})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].N > out[j].N })
	return out
}

// FixSummary is one learned waypoint position with the evidence behind it.
type FixSummary struct {
	Name        string
	Lat, Lon    float64   // the position most of the evidence agrees on
	N           int       // messages that support the place
	First, Last time.Time // when the earliest and the latest of them was received
	Sources     []string  // how the evidence was matched, e.g. "adsc-eta"
}

// FixSummaries lists every learned place, grouped as LearnedFixes groups them, by name. First and Last
// come from the messages that taught it, so they are when it was first heard and last heard again, not
// when we processed them; evidence whose message has since been purged still counts but has no time.
func (s *Store) FixSummaries() ([]FixSummary, error) {
	rows, err := s.db.Query(`SELECT f.name, f.lat, f.lon, f.source, m.ts FROM fix f
		LEFT JOIN message m ON m.id = f.msg_id ORDER BY f.name, f.msg_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ev []fixRow
	for rows.Next() {
		var r fixRow
		if err := rows.Scan(&r.name, &r.lat, &r.lon, &r.source, &r.ts); err != nil {
			return nil, err
		}
		ev = append(ev, r)
	}
	return clusterFixes(ev), rows.Err()
}

// FixNameAt returns the learned waypoint name whose position is within tolDeg of (lat, lon), the
// best supported one if several. It names a waypoint that arrives as bare coordinates.
func (s *Store) FixNameAt(lat, lon, tolDeg float64) (string, bool) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM fix
		WHERE ABS(lat - ?1) <= ?3 AND (ABS(lon - ?2) <= ?3 OR ABS(ABS(lon - ?2) - 360) <= ?3)
		GROUP BY name ORDER BY COUNT(*) DESC, name LIMIT 1`, lat, lon, tolDeg).Scan(&name)
	return name, err == nil
}

// Evidence is one sighting of the flight an aircraft was operating: a flight number in a message, or
// a callsign on an ADS-B position.
type Evidence struct {
	T  time.Time
	ID string // as seen: "CX0113", "CPA113", ...
}

// FlightEvidence returns every sighting of the flight identity in [since, until], oldest first.
// Messages whose text names a different aircraft are left out.
func (s *Store) FlightEvidence(hexAddr string, since, until time.Time) ([]Evidence, error) {
	rows, err := s.db.Query(`
		SELECT ts, CASE WHEN flight != '' THEN flight ELSE callsign END FROM message
		  WHERE hex=?1 AND ts BETWEEN ?2 AND ?3 AND (flight != '' OR callsign != '') AND mismatch NOT LIKE '%which is hex%' AND time_flag=0
		UNION ALL
		SELECT ts, callsign FROM position WHERE hex=?1 AND ts BETWEEN ?2 AND ?3 AND callsign != ''
		ORDER BY 1`, hexAddr, ts(since), ts(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		var e Evidence
		var t int64
		if err := rows.Scan(&t, &e.ID); err != nil {
			return nil, err
		}
		e.T = time.Unix(t, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PositionTimes returns when the aircraft was positioned, from ADS-B and from messages not flagged
// suspect, oldest first.
func (s *Store) PositionTimes(hexAddr string, since, until time.Time) ([]time.Time, error) {
	rows, err := s.db.Query(`
		SELECT ts FROM position WHERE hex=?1 AND ts BETWEEN ?2 AND ?3
		UNION ALL
		SELECT pos_ts FROM message WHERE hex=?1 AND pos_ts BETWEEN ?2 AND ?3 AND lat IS NOT NULL AND suspect = '' AND time_flag = 0
		ORDER BY 1`, hexAddr, ts(since), ts(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, time.Unix(t, 0).UTC())
	}
	return out, rows.Err()
}

// routeReportsSQL selects (t, origin, dest, source) from the kinds of message that say where a flight is going: a
// REP081 report, another A350 report (or its XML maintenance report, or a takeoff report) that names the route, a 787 ACMF report, an airline (label 83) position report and a filed flight plan (arguments:
// hex, from, to).
const routeReportsSQL = `
	SELECT COALESCE(pos_ts, ts) AS t, COALESCE(json_extract(decoded, '$.fields.rep081.Origin'), '') AS origin,
		json_extract(decoded, '$.fields.rep081.Dest') AS dest, 'REP081' AS src
	FROM message WHERE hex=?1 AND kind='miam' AND COALESCE(pos_ts, ts) BETWEEN ?2 AND ?3 AND time_flag=0
	  AND json_extract(decoded, '$.fields.rep081.Dest') IS NOT NULL AND json_extract(decoded, '$.fields.rep081.Dest') != ''
	UNION ALL
	SELECT ts, COALESCE(json_extract(decoded, '$.fields.a350.Origin'), json_extract(decoded, '$.fields.rtr.Origin'), json_extract(decoded, '$.fields.takeoff.Origin'), ''),
		COALESCE(json_extract(decoded, '$.fields.a350.Dest'), json_extract(decoded, '$.fields.rtr.Dest'), json_extract(decoded, '$.fields.takeoff.Dest')), 'A350 report'
	FROM message WHERE hex=?1 AND kind='miam' AND ts BETWEEN ?2 AND ?3 AND time_flag=0
	  AND COALESCE(json_extract(decoded, '$.fields.a350.Dest'), json_extract(decoded, '$.fields.rtr.Dest'), json_extract(decoded, '$.fields.takeoff.Dest'), '') != ''
	UNION ALL
	SELECT ts, json_extract(decoded, '$.fields.departure.Origin'), json_extract(decoded, '$.fields.departure.Dest'), 'airline report'
	FROM message WHERE hex=?1 AND kind IN ('departure_report', 'miam') AND ts BETWEEN ?2 AND ?3 AND time_flag=0
	  AND COALESCE(json_extract(decoded, '$.fields.departure.Dest'), '') != ''
	UNION ALL
	SELECT COALESCE(pos_ts, ts), COALESCE(json_extract(decoded, '$.fields.origin'), ''), json_extract(decoded, '$.fields.dest'),
		CASE kind WHEN 'acmf_report' THEN 'ACMF' ELSE 'airline report' END
	FROM message WHERE hex=?1 AND kind IN ('acmf_report', 'aoc_position', 'posrpt', 'pos_report') AND COALESCE(pos_ts, ts) BETWEEN ?2 AND ?3 AND time_flag=0
	  AND json_extract(decoded, '$.fields.dest') IS NOT NULL AND json_extract(decoded, '$.fields.dest') != ''
	UNION ALL
	SELECT ts, COALESCE(json_extract(decoded, '$.fields.plan.from'), ''), json_extract(decoded, '$.fields.plan.to'), 'flight plan'
	FROM message WHERE hex=?1 AND kind='flight_plan' AND ts BETWEEN ?2 AND ?3 AND time_flag=0
	  AND json_extract(decoded, '$.fields.plan.to') IS NOT NULL AND json_extract(decoded, '$.fields.plan.to') != ''`

// RouteReport is one REP081 origin/destination.
type RouteReport struct {
	T        time.Time
	From, To string
}

// RouteReports returns the REP081 and flight-plan origin/destination reports in [since, until], oldest first.
func (s *Store) RouteReports(hexAddr string, since, until time.Time) ([]RouteReport, error) {
	rows, err := s.db.Query(`SELECT t, origin, dest FROM (`+routeReportsSQL+`) ORDER BY t`, hexAddr, ts(since), ts(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteReport
	for rows.Next() {
		var r RouteReport
		var t int64
		if err := rows.Scan(&t, &r.From, &r.To); err != nil {
			return nil, err
		}
		r.T = time.Unix(t, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountMessages counts an aircraft's stored messages received in [from, to].
func (s *Store) CountMessages(hexAddr string, from, to time.Time) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM message WHERE hex=? AND ts BETWEEN ? AND ?`, hexAddr, ts(from), ts(to)).Scan(&n)
	return n
}

// Departure is a moment an aircraft is known to have left: how we know decides how it is worded.
type Departure struct {
	T   time.Time
	How string // "takeoff" (a takeoff report), "adsb" (seen leaving the ground), "pushback" or "taxi" (a departure report)
}

// timeBefore places a time of day ("hhmm" or "hhmmss", UTC) on the day that puts it at or just before when: a report
// is sent after what it describes, though a station's clock may run a few minutes behind.
func timeBefore(when time.Time, hms string) (time.Time, bool) {
	if len(hms) != 4 && len(hms) != 6 {
		return time.Time{}, false
	}
	n := func(i int) int {
		v, err := strconv.Atoi(hms[i : i+2])
		if err != nil {
			v = 99
		}
		return v
	}
	h, m, sec := n(0), n(2), 0
	if len(hms) == 6 {
		sec = n(4)
	}
	if h > 23 || m > 59 || sec > 59 {
		return time.Time{}, false
	}
	when = when.UTC()
	t := time.Date(when.Year(), when.Month(), when.Day(), h, m, sec, 0, time.UTC)
	for t.After(when.Add(5 * time.Minute)) {
		t = t.AddDate(0, 0, -1)
	}
	return t, true
}

// Departures returns the moments in [since, until] when the aircraft is known to have left: the takeoff time of a takeoff
// report, the gate and taxi times of a departure report, and where ADS-B saw it leave the ground. Oldest first.
func (s *Store) Departures(hexAddr string, since, until time.Time) ([]Departure, error) {
	rows, err := s.db.Query(`
		WITH m AS (SELECT ts, CASE WHEN json_valid(decoded) THEN decoded ELSE '{}' END AS decoded FROM message
		           WHERE hex=?1 AND ts BETWEEN ?2 AND ?3 AND time_flag=0 AND kind IN ('miam', 'departure_report'))
		SELECT ts, 'takeoff', json_extract(decoded, '$.fields.takeoff.Off') FROM m WHERE json_extract(decoded, '$.fields.takeoff.Off') IS NOT NULL
		UNION ALL
		SELECT ts, 'pushback', json_extract(decoded, '$.fields.departure.Events.out."OUT EVENT"') FROM m WHERE json_extract(decoded, '$.fields.departure.Events.out."OUT EVENT"') IS NOT NULL
		UNION ALL
		SELECT ts, 'taxi', json_extract(decoded, '$.fields.departure.Events.taxi.MOVEMENT') FROM m WHERE json_extract(decoded, '$.fields.departure.Events.taxi.MOVEMENT') IS NOT NULL`,
		hexAddr, ts(since), ts(until.Add(time.Hour)))
	if err != nil {
		return nil, err
	}
	var out []Departure
	for rows.Next() {
		var t int64
		var how, hms string
		if err := rows.Scan(&t, &how, &hms); err != nil {
			rows.Close()
			return nil, err
		}
		if d, ok := timeBefore(time.Unix(t, 0), hms); ok && !d.Before(since) && !d.After(until) {
			out = append(out, Departure{d, how})
		}
	}
	rows.Close()

	prows, err := s.db.Query(`SELECT ts, on_ground FROM position WHERE hex=? AND ts BETWEEN ? AND ? ORDER BY ts`, hexAddr, ts(since), ts(until))
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	var prevT int64
	prevGround := false
	for prows.Next() {
		var t int64
		var ground bool
		if err := prows.Scan(&t, &ground); err != nil {
			return nil, err
		}
		if prevGround && !ground && t-prevT <= 15*60 {
			out = append(out, Departure{time.Unix(t, 0).UTC(), "adsb"})
		}
		prevT, prevGround = t, ground
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out, prows.Err()
}
