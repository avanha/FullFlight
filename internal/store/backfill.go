package store

import (
	"sort"
	"time"
)

// HistoryNear is how close to a position we already hold a point from the downloaded history has to be to count as the
// same one: both come from the same receivers a few seconds apart, and keeping both would double the track.
const HistoryNear = 10 * time.Second

// TrackedHexes lists every aircraft we hold anything for: a watch, a position or a message. Upper case, sorted.
func (s *Store) TrackedHexes() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT hex FROM watch WHERE hex <> ''
		UNION SELECT hex FROM position
		UNION SELECT hex FROM message WHERE hex <> ''
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// AddHistory stores points of one aircraft's past track, leaving out any within HistoryNear of a position already held.
// It returns how many it wrote.
func (s *Store) AddHistory(hexAddr string, ps []Position) (int, error) {
	if len(ps) == 0 {
		return 0, nil
	}
	lo, hi := ps[0].T, ps[0].T
	for _, p := range ps {
		if p.T.Before(lo) {
			lo = p.T
		}
		if p.T.After(hi) {
			hi = p.T
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT ts FROM position WHERE hex=? AND ts BETWEEN ? AND ? ORDER BY ts`,
		hexAddr, ts(lo.Add(-HistoryNear)), ts(hi.Add(HistoryNear)))
	if err != nil {
		return 0, err
	}
	var have []int64
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return 0, err
		}
		have = append(have, t)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()
	near := int64(HistoryNear / time.Second)
	wrote := 0
	for _, p := range ps {
		t := ts(p.T)
		if i := sort.Search(len(have), func(i int) bool { return have[i] >= t-near }); i < len(have) && have[i] <= t+near {
			continue
		}
		g := 0
		if p.OnGround {
			g = 1
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO position (hex, ts, lat, lon, alt_ft, on_ground, gs, track, vrate, callsign, reg, source)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			hexAddr, t, p.Lat, p.Lon, p.AltFt, g, p.GS, p.Track, p.VRate, p.Callsign, p.Reg, p.Source)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			wrote++
		}
	}
	return wrote, tx.Commit()
}

// BackfillScanned says which of hexes were already looked for in the history of day (YYYY-MM-DD).
func (s *Store) BackfillScanned(day string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT hex FROM backfill_day WHERE day=?`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// MarkBackfill records that the history of day was searched for hexAddr and gave positions points.
func (s *Store) MarkBackfill(day, hexAddr string, positions int, now time.Time) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO backfill_day (day, hex, positions, scanned_at) VALUES (?,?,?,?)`,
		day, hexAddr, positions, ts(now))
	return err
}
