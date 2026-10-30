package store

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
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
	res, err := s.db.Exec(`INSERT OR IGNORE INTO message (id, hex, ts, label, block_id, msg_num, direction, source_type, station, freq,
		text, dup_key, kind, tail, callsign, flight, summary, detail, decoded, route, lat, lon, alt_ft, pos_ts, suspect,
		af_tail, af_flight, af_dep, af_dest, mismatch)
		VALUES (?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?)`,
		m.ID, m.Hex, ts(m.T), m.Label, m.BlockID, m.MsgNum, m.Direction, m.SourceType, m.Station, m.Freq,
		m.Text, key, m.Kind, m.Tail, m.Callsign, m.Flight, m.Summary, m.Detail, m.Decoded, m.Route, m.Lat, m.Lon, m.AltFt, posT, m.Suspect,
		m.AFTail, m.AFFlight, m.AFDep, m.AFDest, m.Mismatch)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const messageCols = `id, hex, ts, label, block_id, msg_num, direction, source_type, station, freq, text, rx_count,
	kind, tail, callsign, flight, summary, detail, decoded, route, lat, lon, alt_ft, pos_ts, suspect,
	af_tail, af_flight, af_dep, af_dest, mismatch`

// Messages returns an aircraft's messages in [since, until), oldest first,
// limited to the most recent limit rows.
func (s *Store) Messages(hexAddr string, since, until time.Time, limit int) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM message WHERE hex=? AND ts>=? AND ts<?
		ORDER BY ts DESC, id DESC LIMIT ?`, hexAddr, ts(since), ts(until), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var t int64
		var posT *int64
		if err := rows.Scan(&m.ID, &m.Hex, &t, &m.Label, &m.BlockID, &m.MsgNum, &m.Direction, &m.SourceType, &m.Station, &m.Freq,
			&m.Text, &m.RxCount, &m.Kind, &m.Tail, &m.Callsign, &m.Flight, &m.Summary, &m.Detail, &m.Decoded, &m.Route,
			&m.Lat, &m.Lon, &m.AltFt, &posT, &m.Suspect, &m.AFTail, &m.AFFlight, &m.AFDep, &m.AFDest, &m.Mismatch); err != nil {
			return nil, err
		}
		m.T = time.Unix(t, 0).UTC()
		if posT != nil {
			v := time.Unix(*posT, 0).UTC()
			m.PosT = &v
		}
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // oldest first
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
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

// NearestFix is a known position of an aircraft, from ADS-B or a message.
type NearestFix struct {
	Lat, Lon float64
	T        time.Time
	From     string // "adsb" or "message"
}

// NearestFix returns the stored position (ADS-B, or a message position not
// itself flagged suspect) closest in time to t within window, ignoring message
// excludeID.
func (s *Store) NearestFix(hexAddr string, t time.Time, window time.Duration, excludeID int64) (NearestFix, bool) {
	var best NearestFix
	found := false
	consider := func(f NearestFix) {
		d := f.T.Sub(t).Abs()
		if !found || d < best.T.Sub(t).Abs() {
			best, found = f, true
		}
	}
	lo, hi := ts(t.Add(-window)), ts(t.Add(window))
	for _, q := range []struct{ from, sql string }{
		{"adsb", `SELECT lat, lon, ts FROM position WHERE hex=? AND ts BETWEEN ? AND ? ORDER BY ABS(ts-?) LIMIT 1`},
		{"message", `SELECT lat, lon, pos_ts FROM message WHERE hex=? AND pos_ts BETWEEN ? AND ? AND suspect='' AND id<>? ORDER BY ABS(pos_ts-?) LIMIT 1`},
	} {
		var f NearestFix
		var u int64
		var row interface{ Scan(...any) error }
		if q.from == "adsb" {
			row = s.db.QueryRow(q.sql, hexAddr, lo, hi, ts(t))
		} else {
			row = s.db.QueryRow(q.sql, hexAddr, lo, hi, excludeID, ts(t))
		}
		if row.Scan(&f.Lat, &f.Lon, &u) == nil {
			f.T, f.From = time.Unix(u, 0).UTC(), q.from
			consider(f)
		}
	}
	return best, found
}
