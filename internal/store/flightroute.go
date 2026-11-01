package store

import "time"

// ManualRoute is an origin and destination entered by hand for one flight, for when no message gave them.
// Start and End are the span the flight had when it was entered; either airport may be empty.
type ManualRoute struct {
	ID         int64
	Hex        string
	Start, End time.Time
	From, To   string
	Created    time.Time
}

// SetManualRoute records the origin and destination of the flight of hexAddr that spans [start, end], replacing any
// entry already made for it (an entry overlapping the span). With both empty it just removes the entry.
func (s *Store) SetManualRoute(hexAddr string, start, end time.Time, from, to string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM flight_route WHERE hex=? AND start_ts<=? AND end_ts>=?`, hexAddr, ts(end), ts(start)); err != nil {
		return err
	}
	if from != "" || to != "" {
		if _, err := tx.Exec(`INSERT INTO flight_route (hex, start_ts, end_ts, from_code, to_code, created) VALUES (?,?,?,?,?,?)`,
			hexAddr, ts(start), ts(end), from, to, ts(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ManualRoutes returns the entries of an aircraft whose span overlaps [since, until], oldest first.
func (s *Store) ManualRoutes(hexAddr string, since, until time.Time) ([]ManualRoute, error) {
	rows, err := s.db.Query(`SELECT id, hex, start_ts, end_ts, from_code, to_code, created FROM flight_route
		WHERE hex=? AND start_ts<=? AND end_ts>=? ORDER BY start_ts, id`, hexAddr, ts(until), ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManualRoute
	for rows.Next() {
		var m ManualRoute
		var st, en, cr int64
		if err := rows.Scan(&m.ID, &m.Hex, &st, &en, &m.From, &m.To, &cr); err != nil {
			return nil, err
		}
		m.Start, m.End, m.Created = time.Unix(st, 0).UTC(), time.Unix(en, 0).UTC(), time.Unix(cr, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}
