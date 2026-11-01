package store

import "time"

// SetFlightJoin records, or with on false removes, that the flight of hexAddr which started at start is a continuation
// of the flight before it: the two pieces are one flight whatever the messages and positions suggest.
func (s *Store) SetFlightJoin(hexAddr string, start time.Time, on bool, now time.Time) error {
	if !on {
		_, err := s.db.Exec(`DELETE FROM flight_join WHERE hex=? AND start_ts=?`, hexAddr, ts(start))
		return err
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO flight_join (hex, start_ts, created) VALUES (?,?,?)`, hexAddr, ts(start), ts(now))
	return err
}

// FlightJoins returns when the flights joined to the one before them started, within [since, until], oldest first.
func (s *Store) FlightJoins(hexAddr string, since, until time.Time) ([]time.Time, error) {
	rows, err := s.db.Query(`SELECT start_ts FROM flight_join WHERE hex=? AND start_ts BETWEEN ? AND ? ORDER BY start_ts`, hexAddr, ts(since), ts(until))
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
