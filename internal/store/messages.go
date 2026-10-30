package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
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

	// Raw link-layer facts used to work out the direction.
	FromHex, ToHex, AFDir string
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
		af_tail, af_flight, af_dep, af_dest, mismatch, from_hex, to_hex, af_dir)
		VALUES (?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?, ?,?,?)`,
		m.ID, m.Hex, ts(m.T), m.Label, m.BlockID, m.MsgNum, m.Direction, m.SourceType, m.Station, m.Freq,
		m.Text, key, m.Kind, m.Tail, m.Callsign, m.Flight, m.Summary, m.Detail, m.Decoded, m.Route, m.Lat, m.Lon, m.AltFt, posT, m.Suspect,
		m.AFTail, m.AFFlight, m.AFDep, m.AFDest, m.Mismatch, m.FromHex, m.ToHex, m.AFDir)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const messageCols = `id, hex, ts, label, block_id, msg_num, direction, source_type, station, freq, text, rx_count,
	kind, tail, callsign, flight, summary, detail, decoded, route, lat, lon, alt_ft, pos_ts, suspect,
	af_tail, af_flight, af_dep, af_dest, mismatch, from_hex, to_hex, af_dir`

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var t int64
		var posT *int64
		if err := rows.Scan(&m.ID, &m.Hex, &t, &m.Label, &m.BlockID, &m.MsgNum, &m.Direction, &m.SourceType, &m.Station, &m.Freq,
			&m.Text, &m.RxCount, &m.Kind, &m.Tail, &m.Callsign, &m.Flight, &m.Summary, &m.Detail, &m.Decoded, &m.Route,
			&m.Lat, &m.Lon, &m.AltFt, &posT, &m.Suspect, &m.AFTail, &m.AFFlight, &m.AFDep, &m.AFDest, &m.Mismatch, &m.FromHex, &m.ToHex, &m.AFDir); err != nil {
			return nil, err
		}
		m.T = time.Unix(t, 0).UTC()
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
		lat=?, lon=?, alt_ft=?, pos_ts=?, suspect=?, mismatch=? WHERE id=?`,
		m.Direction, m.Kind, m.Tail, m.Callsign, m.Flight, m.Summary, m.Detail, m.Decoded, m.Route,
		m.Lat, m.Lon, m.AltFt, posT, m.Suspect, m.Mismatch, m.ID)
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
// that a coherent run of fixes can vouch for one another.
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
	const msg = `SELECT lat, lon, pos_ts FROM message WHERE hex=? AND pos_ts BETWEEN ? AND ? AND lat IS NOT NULL AND id<>? ORDER BY pos_ts %s LIMIT ?`
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
// aircraft, skipping ones flagged suspect.
func (s *Store) LastMessagePosition(hexAddr string) (MessagePosition, bool) {
	var p MessagePosition
	var t int64
	err := s.db.QueryRow(`SELECT pos_ts, kind, lat, lon, alt_ft FROM message
		WHERE hex=? AND pos_ts IS NOT NULL AND lat IS NOT NULL AND suspect=''
		ORDER BY pos_ts DESC LIMIT 1`, hexAddr).Scan(&t, &p.Kind, &p.Lat, &p.Lon, &p.AltFt)
	if err != nil {
		return MessagePosition{}, false
	}
	p.T = time.Unix(t, 0).UTC()
	return p, true
}
