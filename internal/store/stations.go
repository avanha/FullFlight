package store

import (
	"database/sql"
	"sort"
	"time"
)

// StationInfo is what airframes.io says about a receiving station.
type StationInfo struct {
	Ident          string
	Lat, Lon       *float64
	Fuzzed         bool // the position is approximate: the station asked for that
	CountryCode    string
	CountryName    string
	SourceType     string // vdl, acars, hfdl, ...
	Application    string // dumpvdl2, acarsdec, ...
	Status         string
	Flagged        bool // airframes.io's own mark, with its reason
	FlaggedReason  string
	LastReportAt   time.Time
	MessagesCount  int64 // all it has ever sent airframes.io
	NearestAirport string
}

// SaveStations replaces what is kept about the stations in infos, and notes when the list was fetched.
func (s *Store) SaveStations(infos []StationInfo, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT OR REPLACE INTO station (ident, lat, lon, fuzzed, country_code, country_name, source_type, application, status,
		flagged, flagged_reason, last_report_at, messages_count, nearest_airport, fetched_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, i := range infos {
		var lastReport int64
		if !i.LastReportAt.IsZero() {
			lastReport = i.LastReportAt.Unix()
		}
		if _, err := st.Exec(i.Ident, i.Lat, i.Lon, boolInt(i.Fuzzed), i.CountryCode, i.CountryName, i.SourceType, i.Application, i.Status,
			boolInt(i.Flagged), i.FlaggedReason, lastReport, i.MessagesCount, i.NearestAirport, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StationsFetched is when the list of stations was last fetched, or the zero time if never.
func (s *Store) StationsFetched() time.Time {
	var t sql.NullInt64
	s.db.QueryRow(`SELECT MAX(fetched_at) FROM station`).Scan(&t)
	if !t.Valid {
		return time.Time{}
	}
	return time.Unix(t.Int64, 0).UTC()
}

// Lateness is how late a station's messages arrive, from those that carry the aircraft's own time: how long after the aircraft
// said it was happening each reached us.
type Lateness struct {
	Timed    int           // messages with a time of the aircraft's own
	Late     int           // of them, more than LateAfter after it
	Median   time.Duration // of the lags
	P90      time.Duration
	Shortest time.Duration // the least lag of any: a station whose shortest is hours long never delivers a message promptly
}

// LateAfter is how long after the aircraft's own time a message has to arrive to count as late.
const LateAfter = time.Hour

// StationStats is what we know of one station from the messages we hold, with what airframes.io says about it.
type StationStats struct {
	Ident      string
	Messages   int
	Aircraft   int
	First      time.Time
	Last       time.Time
	Transports map[string]int // by source type
	Lateness   Lateness
	Info       *StationInfo // nil if the list has not been fetched, or does not have it
}

// Stations returns every station any of our messages came through, with its counts and lateness and airframes.io's details.
func (s *Store) Stations() ([]StationStats, error) {
	rows, err := s.db.Query(`SELECT station, COUNT(*), COUNT(DISTINCT hex), MIN(ts), MAX(ts) FROM message WHERE station <> '' GROUP BY station`)
	if err != nil {
		return nil, err
	}
	by := map[string]*StationStats{}
	var out []*StationStats
	for rows.Next() {
		var st StationStats
		var first, last int64
		if err := rows.Scan(&st.Ident, &st.Messages, &st.Aircraft, &first, &last); err != nil {
			rows.Close()
			return nil, err
		}
		st.First, st.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		st.Transports = map[string]int{}
		by[st.Ident] = &st
		out = append(out, &st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if rows, err = s.db.Query(`SELECT station, source_type, COUNT(*) FROM message WHERE station <> '' GROUP BY station, source_type`); err != nil {
		return nil, err
	}
	for rows.Next() {
		var ident, typ string
		var n int
		if err := rows.Scan(&ident, &typ, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if st := by[ident]; st != nil {
			st.Transports[typ] = n
		}
	}
	rows.Close()

	// the lags: how long after the time the aircraft put in a message it reached us
	if rows, err = s.db.Query(`SELECT station, ts - ` + eventAt + ` FROM message WHERE station <> '' AND time_flag = 0 AND ` + eventAt + ` IS NOT NULL`); err != nil {
		return nil, err
	}
	lags := map[string][]time.Duration{}
	for rows.Next() {
		var ident string
		var lag int64
		if err := rows.Scan(&ident, &lag); err != nil {
			rows.Close()
			return nil, err
		}
		if lag < 0 {
			lag = 0 // stamped before the aircraft's own time: a clock a little out, not a message from the future
		}
		lags[ident] = append(lags[ident], time.Duration(lag)*time.Second)
	}
	rows.Close()
	for ident, l := range lags {
		if st := by[ident]; st != nil {
			st.Lateness = lateness(l)
		}
	}

	infos, err := s.stationInfos()
	if err != nil {
		return nil, err
	}
	res := make([]StationStats, 0, len(out))
	for _, st := range out {
		if i, ok := infos[st.Ident]; ok {
			st.Info = &i
		}
		res = append(res, *st)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Messages > res[j].Messages })
	return res, nil
}

func lateness(lags []time.Duration) Lateness {
	sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
	l := Lateness{Timed: len(lags), Shortest: lags[0], Median: lags[len(lags)/2], P90: lags[(len(lags)*9)/10]}
	for _, d := range lags {
		if d > LateAfter {
			l.Late++
		}
	}
	return l
}

func (s *Store) stationInfos() (map[string]StationInfo, error) {
	rows, err := s.db.Query(`SELECT ident, lat, lon, fuzzed, country_code, country_name, source_type, application, status, flagged, flagged_reason,
		last_report_at, messages_count, nearest_airport FROM station`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StationInfo{}
	for rows.Next() {
		var i StationInfo
		var fuzzed, flagged int
		var last int64
		if err := rows.Scan(&i.Ident, &i.Lat, &i.Lon, &fuzzed, &i.CountryCode, &i.CountryName, &i.SourceType, &i.Application, &i.Status, &flagged,
			&i.FlaggedReason, &last, &i.MessagesCount, &i.NearestAirport); err != nil {
			return nil, err
		}
		i.Fuzzed, i.Flagged = fuzzed == 1, flagged == 1
		if last > 0 {
			i.LastReportAt = time.Unix(last, 0).UTC()
		}
		out[i.Ident] = i
	}
	return out, rows.Err()
}
