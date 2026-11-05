package store

import "time"

// CountRequest adds one request we made to host to the day's tally. status is 0 when there was no answer at all.
func (s *Store) CountRequest(at time.Time, host string, status int, failed bool) error {
	f, l := 0, 0
	if failed {
		f = 1
	}
	if status == 429 {
		l = 1
	}
	_, err := s.db.Exec(`INSERT INTO request_count (day, host, requests, failed, limited) VALUES (?, ?, 1, ?, ?)
		ON CONFLICT (day, host) DO UPDATE SET requests = requests + 1, failed = failed + ?, limited = limited + ?`,
		at.UTC().Format("2006-01-02"), host, f, l, f, l)
	return err
}

// RequestCount is how many requests were made to one host on one UTC day.
type RequestCount struct {
	Day                       string
	Host                      string
	Requests, Failed, Limited int
}

// RequestCounts returns the tallies from the day of since on, oldest day first; the zero time asks for all of them.
func (s *Store) RequestCounts(since time.Time) ([]RequestCount, error) {
	from := ""
	if !since.IsZero() {
		from = since.UTC().Format("2006-01-02")
	}
	rows, err := s.db.Query(`SELECT day, host, requests, failed, limited FROM request_count WHERE day >= ? ORDER BY day, host`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestCount
	for rows.Next() {
		var r RequestCount
		if err := rows.Scan(&r.Day, &r.Host, &r.Requests, &r.Failed, &r.Limited); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DayCount is a number of rows stored on one UTC day.
type DayCount struct {
	Day       string
	Messages  int
	Positions int
}

// Holdings is how much of each kind of thing the database holds, for the stats page.
type Holdings struct {
	Messages, Messages24h, Messages7d    int
	Positions, Positions24h, Positions7d int
	MessagesBy                           map[string]int // by link: source_type (vdl, acars, aero-acars, ...), "" for none
	PositionsBy                          map[string]int // by source: adsb.lol, adsb.fi, history, aerodatabox, ...
	Decoded, Undecoded                   int            // messages we read into a kind, and those we left as "unknown"
	Aircraft                             int            // distinct aircraft with a message or a position
	Watches                              map[string]int // by status
	Airframes, Notes, Stations, WxTiles  int
	WxBytes                              int64
	Days                                 []DayCount // the last 7 UTC days, oldest first, by when the row was stamped
}

// Holdings counts what the database holds as of now.
func (s *Store) Holdings(now time.Time) (Holdings, error) {
	h := Holdings{MessagesBy: map[string]int{}, PositionsBy: map[string]int{}, Watches: map[string]int{}}
	d24, d7 := now.Add(-24*time.Hour).Unix(), now.Add(-7*24*time.Hour).Unix()
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ts >= ?1), 0), COALESCE(SUM(ts >= ?2), 0), COALESCE(SUM(kind != 'unknown' AND kind != ''), 0) FROM message`, d24, d7).
		Scan(&h.Messages, &h.Messages24h, &h.Messages7d, &h.Decoded); err != nil {
		return h, err
	}
	h.Undecoded = h.Messages - h.Decoded
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ts >= ?1), 0), COALESCE(SUM(ts >= ?2), 0) FROM position`, d24, d7).
		Scan(&h.Positions, &h.Positions24h, &h.Positions7d); err != nil {
		return h, err
	}
	for _, q := range []struct {
		sql string
		to  map[string]int
	}{
		{`SELECT source_type, COUNT(*) FROM message GROUP BY 1`, h.MessagesBy},
		{`SELECT source, COUNT(*) FROM position GROUP BY 1`, h.PositionsBy},
		{`SELECT status, COUNT(*) FROM watch GROUP BY 1`, h.Watches},
	} {
		rows, err := s.db.Query(q.sql)
		if err != nil {
			return h, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return h, err
			}
			q.to[k] = n
		}
		rows.Close()
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT hex FROM message UNION SELECT hex FROM position)`).Scan(&h.Aircraft)
	s.db.QueryRow(`SELECT COUNT(*) FROM airframe`).Scan(&h.Airframes)
	s.db.QueryRow(`SELECT COUNT(*) FROM airframe_note`).Scan(&h.Notes)
	s.db.QueryRow(`SELECT COUNT(*) FROM station`).Scan(&h.Stations)
	s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM wx_tile`).Scan(&h.WxTiles, &h.WxBytes)

	byDay := map[string]*DayCount{}
	for i := 6; i >= 0; i-- {
		d := now.UTC().AddDate(0, 0, -i).Format("2006-01-02")
		h.Days = append(h.Days, DayCount{Day: d})
		byDay[d] = &h.Days[len(h.Days)-1]
	}
	for _, q := range []struct {
		sql string
		set func(*DayCount, int)
	}{
		{`SELECT date(ts, 'unixepoch'), COUNT(*) FROM message WHERE ts >= ? GROUP BY 1`, func(d *DayCount, n int) { d.Messages = n }},
		{`SELECT date(ts, 'unixepoch'), COUNT(*) FROM position WHERE ts >= ? GROUP BY 1`, func(d *DayCount, n int) { d.Positions = n }},
	} {
		rows, err := s.db.Query(q.sql, now.UTC().AddDate(0, 0, -6).Truncate(24*time.Hour).Unix())
		if err != nil {
			return h, err
		}
		for rows.Next() {
			var day string
			var n int
			if err := rows.Scan(&day, &n); err != nil {
				rows.Close()
				return h, err
			}
			if d := byDay[day]; d != nil {
				q.set(d, n)
			}
		}
		rows.Close()
	}
	return h, nil
}
