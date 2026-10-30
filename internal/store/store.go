// Package store persists the watchlist and the ADS-B track log in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/avanha/fullflight/internal/watchlist"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS watch (
	id       INTEGER PRIMARY KEY,
	kind     TEXT NOT NULL,
	ident    TEXT NOT NULL,
	date     TEXT NOT NULL DEFAULT '',
	start_ts INTEGER NOT NULL,
	end_ts   INTEGER NOT NULL,
	status   TEXT NOT NULL,
	hex      TEXT NOT NULL DEFAULT '',
	tail     TEXT NOT NULL DEFAULT '',
	callsign TEXT NOT NULL DEFAULT '',
	UNIQUE (kind, ident, date, start_ts)
);
CREATE TABLE IF NOT EXISTS position (
	hex      TEXT NOT NULL,
	ts       INTEGER NOT NULL,
	lat      REAL NOT NULL,
	lon      REAL NOT NULL,
	alt_ft   REAL,
	on_ground INTEGER NOT NULL DEFAULT 0,
	gs       REAL,
	track    REAL,
	vrate    REAL,
	callsign TEXT NOT NULL DEFAULT '',
	reg      TEXT NOT NULL DEFAULT '',
	source   TEXT NOT NULL,
	PRIMARY KEY (hex, ts)
) WITHOUT ROWID;
`

// Open opens (creating if needed) the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; keeps :memory: coherent too
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ts(t time.Time) int64 { return t.Unix() }

// AddWatch inserts an entry and returns it with its ID. Adding the same
// entry twice returns the existing one.
func (s *Store) AddWatch(e watchlist.Entry) (watchlist.Entry, error) {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO watch (kind, ident, date, start_ts, end_ts, status, hex, tail, callsign)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		e.Kind, e.Ident, e.Date, ts(e.Start), ts(e.End), e.Status, e.Hex, e.Tail, e.Callsign)
	if err != nil {
		return e, err
	}
	row := s.db.QueryRow(watchSelect+` WHERE kind=? AND ident=? AND date=? AND start_ts=?`, e.Kind, e.Ident, e.Date, ts(e.Start))
	return scanWatch(row)
}

const watchSelect = `SELECT id, kind, ident, date, start_ts, end_ts, status, hex, tail, callsign FROM watch`

type scanner interface{ Scan(...any) error }

func scanWatch(r scanner) (watchlist.Entry, error) {
	var e watchlist.Entry
	var start, end int64
	err := r.Scan(&e.ID, &e.Kind, &e.Ident, &e.Date, &start, &end, &e.Status, &e.Hex, &e.Tail, &e.Callsign)
	e.Start, e.End = time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
	return e, err
}

// ListWatches returns all entries, newest first. If openOnly, only waiting/tracking ones.
func (s *Store) ListWatches(openOnly bool) ([]watchlist.Entry, error) {
	q := watchSelect
	if openOnly {
		q += ` WHERE status IN ('waiting','tracking')`
	}
	rows, err := s.db.Query(q + ` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []watchlist.Entry
	for rows.Next() {
		e, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateWatch writes back the mutable fields: status, end, hex, tail, callsign.
func (s *Store) UpdateWatch(e watchlist.Entry) error {
	_, err := s.db.Exec(`UPDATE watch SET status=?, end_ts=?, hex=?, tail=?, callsign=? WHERE id=?`,
		e.Status, ts(e.End), e.Hex, e.Tail, e.Callsign, e.ID)
	return err
}

func (s *Store) DeleteWatch(id int64) error {
	_, err := s.db.Exec(`DELETE FROM watch WHERE id=?`, id)
	return err
}

// ExpireWatches marks open entries past their end as expired, or done if they
// were tracking an aircraft. It returns how many changed.
func (s *Store) ExpireWatches(now time.Time) (int64, error) {
	res, err := s.db.Exec(`UPDATE watch SET status = CASE WHEN status='tracking' THEN 'done' ELSE 'expired' END
		WHERE status IN ('waiting','tracking') AND end_ts <= ?`, ts(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type Position struct {
	Hex      string
	T        time.Time
	Lat, Lon float64
	AltFt    *float64
	OnGround bool
	GS       *float64
	Track    *float64
	VRate    *float64
	Callsign string
	Reg      string
	Source   string
}

// AddPosition stores p unless it duplicates the latest stored point for the
// aircraft (same time, or same place). It reports whether a row was written.
func (s *Store) AddPosition(p Position) (bool, error) {
	var lat, lon float64
	var last int64
	err := s.db.QueryRow(`SELECT lat, lon, ts FROM position WHERE hex=? ORDER BY ts DESC LIMIT 1`, p.Hex).Scan(&lat, &lon, &last)
	if err == nil && (last == ts(p.T) || (lat == p.Lat && lon == p.Lon)) {
		return false, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	g := 0
	if p.OnGround {
		g = 1
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO position (hex, ts, lat, lon, alt_ft, on_ground, gs, track, vrate, callsign, reg, source)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Hex, ts(p.T), p.Lat, p.Lon, p.AltFt, g, p.GS, p.Track, p.VRate, p.Callsign, p.Reg, p.Source)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Track returns an aircraft's positions in [since, until), oldest first.
func (s *Store) Track(hex string, since, until time.Time) ([]Position, error) {
	rows, err := s.db.Query(`SELECT hex, ts, lat, lon, alt_ft, on_ground, gs, track, vrate, callsign, reg, source
		FROM position WHERE hex=? AND ts>=? AND ts<? ORDER BY ts`, hex, ts(since), ts(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Position
	for rows.Next() {
		var p Position
		var t int64
		var g int
		if err := rows.Scan(&p.Hex, &t, &p.Lat, &p.Lon, &p.AltFt, &g, &p.GS, &p.Track, &p.VRate, &p.Callsign, &p.Reg, &p.Source); err != nil {
			return nil, err
		}
		p.T, p.OnGround = time.Unix(t, 0).UTC(), g == 1
		out = append(out, p)
	}
	return out, rows.Err()
}
