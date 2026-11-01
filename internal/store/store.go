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

// Migrations run in order; PRAGMA user_version records how many have run.
var migrations = []string{
	`ALTER TABLE watch ADD COLUMN airborne  INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE watch ADD COLUMN landed_ts INTEGER NOT NULL DEFAULT 0;
	 -- hex <-> registration. Kept forever; a hex can be re-registered, so a
	 -- hex may have several rows and the newest last_seen wins.
	 CREATE TABLE airframe (
		hex        TEXT NOT NULL,
		tail       TEXT NOT NULL,
		icao_type  TEXT NOT NULL DEFAULT '',
		source     TEXT NOT NULL,
		first_seen INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL,
		PRIMARY KEY (hex, tail)
	 ) WITHOUT ROWID;
	 CREATE INDEX airframe_tail ON airframe (tail);`,
	`ALTER TABLE watch ADD COLUMN dest TEXT NOT NULL DEFAULT '';`,
	`CREATE TABLE message (
		id          INTEGER PRIMARY KEY,   -- airframes.io message id
		hex         TEXT NOT NULL,         -- the aircraft we fetched it for
		ts          INTEGER NOT NULL,      -- receive time, unix seconds UTC
		label       TEXT NOT NULL DEFAULT '',
		block_id    TEXT NOT NULL DEFAULT '',
		msg_num     TEXT NOT NULL DEFAULT '',
		direction   TEXT NOT NULL DEFAULT '',
		source_type TEXT NOT NULL DEFAULT '',
		station     TEXT NOT NULL DEFAULT '',
		freq        REAL,
		text        TEXT NOT NULL,         -- raw, exactly as received
		dup_key     TEXT NOT NULL,
		rx_count    INTEGER NOT NULL DEFAULT 1,
		-- our decode
		kind        TEXT NOT NULL DEFAULT '',
		tail        TEXT NOT NULL DEFAULT '',
		callsign    TEXT NOT NULL DEFAULT '',
		flight      TEXT NOT NULL DEFAULT '',
		summary     TEXT NOT NULL DEFAULT '',
		detail      TEXT NOT NULL DEFAULT '',
		decoded     TEXT NOT NULL DEFAULT '',   -- JSON: libacars tree + our fields
		route       TEXT NOT NULL DEFAULT '',   -- JSON: ADS-C predicted waypoints
		lat         REAL,
		lon         REAL,
		alt_ft      REAL,
		pos_ts      INTEGER,
		suspect     TEXT NOT NULL DEFAULT '',   -- why the position looks wrong, if it does
		-- airframes.io's own opinion, kept apart from ours
		af_tail     TEXT NOT NULL DEFAULT '',
		af_flight   TEXT NOT NULL DEFAULT '',
		af_dep      TEXT NOT NULL DEFAULT '',
		af_dest     TEXT NOT NULL DEFAULT '',
		mismatch    TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX message_hex_ts ON message (hex, ts);
	CREATE INDEX message_dup ON message (dup_key, ts);
	CREATE TABLE fetch_state (
		hex        TEXT PRIMARY KEY,
		fetched_to INTEGER NOT NULL   -- the end of the last successful fetch, unix seconds
	) WITHOUT ROWID;`,
	// Who sent it and what airframes.io said about direction, so a later
	// re-decode can work out uplink vs downlink without guessing.
	`ALTER TABLE message ADD COLUMN from_hex TEXT NOT NULL DEFAULT '';
	 ALTER TABLE message ADD COLUMN to_hex   TEXT NOT NULL DEFAULT '';
	 ALTER TABLE message ADD COLUMN af_dir   TEXT NOT NULL DEFAULT '';`,
	// Where a message carries its own time that can be compared with its arrival
	// time: both, and whether they disagree by enough to distrust the placement.
	`ALTER TABLE message ADD COLUMN time_note TEXT NOT NULL DEFAULT '';
	 ALTER TABLE message ADD COLUMN time_flag INTEGER NOT NULL DEFAULT 0;`,
	// Weather at the report, the speed and track it gave, and the waypoints a CPDLC position report
	// names. Plus the waypoint coordinates we have learned: a name matched to an ADS-C predicted
	// waypoint by its ETA, one row of evidence per message so that learning again changes nothing.
	`ALTER TABLE message ADD COLUMN wx_dir  REAL;
	 ALTER TABLE message ADD COLUMN wx_kt   REAL;
	 ALTER TABLE message ADD COLUMN wx_temp REAL;
	 ALTER TABLE message ADD COLUMN gs      REAL;
	 ALTER TABLE message ADD COLUMN trk     REAL;
	 ALTER TABLE message ADD COLUMN fixes   TEXT NOT NULL DEFAULT '';
	 CREATE TABLE fix (
		name    TEXT NOT NULL,
		msg_id  INTEGER NOT NULL,
		lat     REAL NOT NULL,
		lon     REAL NOT NULL,
		source  TEXT NOT NULL,
		PRIMARY KEY (name, msg_id)
	 ) WITHOUT ROWID;`, // Callsigns of airlines we had no table entry for (TZP1, CKK624) used to be filed as tail watches and
	// looked up as registrations. Nothing without a hyphen that is three letters then digits can be a
	// registration, so any such watch that never found an aircraft is a callsign: make it one.
	`UPDATE OR IGNORE watch SET kind='flight', tail='',
		status = CASE WHEN status='tracking' THEN 'waiting' ELSE status END
	 WHERE kind='tail' AND hex='' AND (
		ident GLOB '[A-Z][A-Z][A-Z][0-9]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][A-Z]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][0-9]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][0-9][A-Z]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][0-9][0-9]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][0-9][0-9][A-Z]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][0-9][0-9][0-9]'
		OR ident GLOB '[A-Z][A-Z][A-Z][0-9][0-9][0-9][0-9][A-Z]'
	 );`,
	// Notes about an airframe, as many as you like, each with its own text and times. An airframe that has
	// any is never deleted: the database itself refuses (the airframe table is otherwise never pruned, but
	// this holds for any future clean-up, or a hand-typed DELETE, too). Notes belong to the hex, which is
	// the aircraft, not to a registration, which can change.
	`CREATE TABLE IF NOT EXISTS airframe_note (
		id      INTEGER PRIMARY KEY,
		hex     TEXT NOT NULL,
		note    TEXT NOT NULL,
		created INTEGER NOT NULL,
		updated INTEGER NOT NULL
	 );
	 CREATE INDEX IF NOT EXISTS airframe_note_hex ON airframe_note (hex);
	 CREATE TRIGGER IF NOT EXISTS airframe_keeps_notes BEFORE DELETE ON airframe
	 WHEN EXISTS (SELECT 1 FROM airframe_note WHERE hex = OLD.hex)
	 BEGIN
		SELECT RAISE(ABORT, 'airframe has notes and is never purged');
	 END;`,
	// What the last ADS-B ask for a watch found, so that "why is there no track?" can be answered from the page:
	// when it was asked, what came back, and when it last gave a position. Goes when the watch goes.
	`CREATE TABLE IF NOT EXISTS poll_state (
		watch_id INTEGER PRIMARY KEY REFERENCES watch (id) ON DELETE CASCADE,
		asked_ts INTEGER NOT NULL,
		outcome  TEXT NOT NULL,            -- heard, not_seen, no_position or error
		detail   TEXT NOT NULL DEFAULT '', -- the error, or why an answer had no usable position
		heard_ts INTEGER NOT NULL DEFAULT 0, -- when the newest position it ever gave was taken (0: never)
		source   TEXT NOT NULL DEFAULT '', -- who gave that
		misses   INTEGER NOT NULL DEFAULT 0 -- asks since then that found it absent, or without a position
	 );`,
	// An origin and destination typed in by hand for a flight whose messages never gave one. The flight is
	// remembered by the span it had when the entry was made; it applies to whichever flight covers that time.
	`CREATE TABLE IF NOT EXISTS flight_route (
		id        INTEGER PRIMARY KEY,
		hex       TEXT NOT NULL,
		start_ts  INTEGER NOT NULL,
		end_ts    INTEGER NOT NULL,
		from_code TEXT NOT NULL DEFAULT '',
		to_code   TEXT NOT NULL DEFAULT '',
		created   INTEGER NOT NULL
	 );
	 CREATE INDEX IF NOT EXISTS flight_route_hex ON flight_route (hex, start_ts);`,
}

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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func ts(t time.Time) int64 { return t.Unix() }

// AddWatch adds an entry and returns it with its ID. If an open (waiting, tracking or paused) entry for the same
// kind, identifier and date already exists, that one is updated instead: it takes the destination if the new entry
// has one and the later of the two end times, and a paused one is resumed. Finished entries do not count, so
// watching the same flight again starts a new one. A tail and a hex that are the same aircraft count as the same
// watch (see sameAircraftWatch).
func (s *Store) AddWatch(e watchlist.Entry) (watchlist.Entry, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM watch WHERE kind=? AND ident=? AND date=? AND status IN ('waiting','tracking','paused')
		ORDER BY id DESC LIMIT 1`, e.Kind, e.Ident, e.Date).Scan(&id)
	if err == sql.ErrNoRows {
		id, err = s.sameAircraftWatch(e)
	}
	if err == nil {
		// Watching again something that is paused resumes it.
		if _, err := s.db.Exec(`UPDATE watch SET dest = CASE WHEN ? != '' THEN ? ELSE dest END, end_ts = MAX(end_ts, ?),
			status = CASE WHEN status='paused' THEN (CASE WHEN hex != '' THEN 'tracking' ELSE 'waiting' END) ELSE status END WHERE id=?`,
			e.Dest, e.Dest, ts(e.End), id); err != nil {
			return e, err
		}
		return scanWatch(s.db.QueryRow(watchSelect+` WHERE id=?`, id))
	}
	if err != sql.ErrNoRows {
		return e, err
	}
	res, err := s.db.Exec(`INSERT INTO watch (kind, ident, date, start_ts, end_ts, status, hex, tail, callsign, dest)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.Kind, e.Ident, e.Date, ts(e.Start), ts(e.End), e.Status, e.Hex, e.Tail, e.Callsign, e.Dest)
	if err != nil {
		return e, err
	}
	id, _ = res.LastInsertId()
	return scanWatch(s.db.QueryRow(watchSelect+` WHERE id=?`, id))
}

// sameAircraftWatch finds an open tail or hex watch on the same aircraft as e, which is a tail or hex
// watch: a registration and an ICAO address name one airframe, so watching both would poll it twice and
// list it twice. The hex is the key. A tail whose hex we do not know yet matches only itself.
func (s *Store) sameAircraftWatch(e watchlist.Entry) (int64, error) {
	if e.Kind != watchlist.KindTail && e.Kind != watchlist.KindHex {
		return 0, sql.ErrNoRows
	}
	hex := e.Hex
	if e.Kind == watchlist.KindHex {
		hex = e.Ident
	} else if hex == "" {
		hex, _ = s.HexForTail(e.Ident)
	}
	if hex == "" {
		return 0, sql.ErrNoRows
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM watch WHERE kind IN ('tail','hex') AND status IN ('waiting','tracking','paused')
		AND (hex=? OR (kind='hex' AND ident=?) OR (kind='tail' AND ident IN (SELECT tail FROM airframe WHERE hex=?)))
		ORDER BY id DESC LIMIT 1`, hex, hex, hex).Scan(&id)
	return id, err
}

const watchSelect = `SELECT id, kind, ident, date, start_ts, end_ts, status, hex, tail, callsign, airborne, landed_ts, dest FROM watch`

type scanner interface{ Scan(...any) error }

func scanWatch(r scanner) (watchlist.Entry, error) {
	var e watchlist.Entry
	var start, end, landed int64
	var airborne int
	err := r.Scan(&e.ID, &e.Kind, &e.Ident, &e.Date, &start, &end, &e.Status, &e.Hex, &e.Tail, &e.Callsign, &airborne, &landed, &e.Dest)
	e.Start, e.End = time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
	e.Airborne = airborne == 1
	if landed != 0 {
		e.LandedAt = time.Unix(landed, 0).UTC()
	}
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

// UpdateWatch writes back the mutable fields.
func (s *Store) UpdateWatch(e watchlist.Entry) error {
	var landed int64
	if !e.LandedAt.IsZero() {
		landed = ts(e.LandedAt)
	}
	airborne := 0
	if e.Airborne {
		airborne = 1
	}
	_, err := s.db.Exec(`UPDATE watch SET status=?, end_ts=?, dest=?, hex=?, tail=?, callsign=?, airborne=?, landed_ts=? WHERE id=?`,
		e.Status, ts(e.End), e.Dest, e.Hex, e.Tail, e.Callsign, airborne, landed, e.ID)
	return err
}

// GetWatch returns one entry, or sql.ErrNoRows.
func (s *Store) GetWatch(id int64) (watchlist.Entry, error) {
	return scanWatch(s.db.QueryRow(watchSelect+` WHERE id=?`, id))
}

func (s *Store) DeleteWatch(id int64) error {
	// (the foreign key would do it, but it is only switched on for a file database)
	if _, err := s.db.Exec(`DELETE FROM poll_state WHERE watch_id=?`, id); err != nil {
		return err
	}
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

// LastPosition returns the newest stored position for an aircraft.
func (s *Store) LastPosition(hex string) (Position, bool) {
	t := s.lastPosition(hex)
	return t, t.Hex != ""
}

// LastCallsign returns the callsign on the aircraft's newest ADS-B position that has one.
func (s *Store) LastCallsign(hexAddr string) (string, time.Time, bool) {
	var cs string
	var t int64
	if err := s.db.QueryRow(`SELECT callsign, ts FROM position WHERE hex=? AND callsign != '' ORDER BY ts DESC LIMIT 1`, hexAddr).Scan(&cs, &t); err != nil {
		return "", time.Time{}, false
	}
	return cs, time.Unix(t, 0).UTC(), true
}

// lastPosition is the newest position or the zero value.
func (s *Store) lastPosition(hex string) Position {
	var p Position
	var t int64
	var g int
	err := s.db.QueryRow(`SELECT hex, ts, lat, lon, alt_ft, on_ground, gs, track, vrate, callsign, reg, source
		FROM position WHERE hex=? ORDER BY ts DESC LIMIT 1`, hex).
		Scan(&p.Hex, &t, &p.Lat, &p.Lon, &p.AltFt, &g, &p.GS, &p.Track, &p.VRate, &p.Callsign, &p.Reg, &p.Source)
	if err != nil {
		return Position{}
	}
	p.T, p.OnGround = time.Unix(t, 0).UTC(), g == 1
	return p
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

// Airframe is one observed hex <-> registration pairing.
type Airframe struct {
	Hex, Tail, Type, Source string
	FirstSeen, LastSeen     time.Time
}

// RecordAirframe remembers a hex/tail pairing, bumping last_seen if known.
// Empty hex or tail is ignored. These rows are never deleted.
func (s *Store) RecordAirframe(a Airframe) error {
	if a.Hex == "" || a.Tail == "" {
		return nil
	}
	if a.LastSeen.IsZero() {
		a.LastSeen = time.Now()
	}
	if a.FirstSeen.IsZero() {
		a.FirstSeen = a.LastSeen
	}
	_, err := s.db.Exec(`INSERT INTO airframe (hex, tail, icao_type, source, first_seen, last_seen) VALUES (?,?,?,?,?,?)
		ON CONFLICT (hex, tail) DO UPDATE SET
			last_seen = MAX(last_seen, excluded.last_seen),
			icao_type = CASE WHEN excluded.icao_type != '' THEN excluded.icao_type ELSE icao_type END`,
		a.Hex, a.Tail, a.Type, a.Source, ts(a.FirstSeen), ts(a.LastSeen))
	return err
}

// HexForTail returns the most recently seen hex for a registration.
func (s *Store) HexForTail(tail string) (string, bool) {
	var hex string
	err := s.db.QueryRow(`SELECT hex FROM airframe WHERE tail=? ORDER BY last_seen DESC LIMIT 1`, tail).Scan(&hex)
	return hex, err == nil
}

// TailForHex returns the most recently seen registration for a hex.
func (s *Store) TailForHex(hex string) (string, bool) {
	var tail string
	err := s.db.QueryRow(`SELECT tail FROM airframe WHERE hex=? ORDER BY last_seen DESC LIMIT 1`, hex).Scan(&tail)
	return tail, err == nil
}

// TypeForHex returns the aircraft type code (e.g. "A359") last recorded for a hex.
func (s *Store) TypeForHex(hex string) string {
	var t string
	s.db.QueryRow(`SELECT icao_type FROM airframe WHERE hex=? AND icao_type != '' ORDER BY last_seen DESC LIMIT 1`, hex).Scan(&t)
	return t
}

func (s *Store) ListAirframes() ([]Airframe, error) {
	rows, err := s.db.Query(`SELECT hex, tail, icao_type, source, first_seen, last_seen FROM airframe ORDER BY tail, last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Airframe
	for rows.Next() {
		var a Airframe
		var f, l int64
		if err := rows.Scan(&a.Hex, &a.Tail, &a.Type, &a.Source, &f, &l); err != nil {
			return nil, err
		}
		a.FirstSeen, a.LastSeen = time.Unix(f, 0).UTC(), time.Unix(l, 0).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// Poll outcomes: what asking the ADS-B sources about a watch found.
const (
	PollHeard      = "heard"       // a position, fresh enough to store
	PollNotSeen    = "not_seen"    // the sources answered and the aircraft was not in the answer
	PollNoPosition = "no_position" // it was in the answer, without a position we can use
	PollError      = "error"       // no source could answer
)

// PollState is what the last ADS-B ask for a watch found.
type PollState struct {
	Asked   time.Time
	Outcome string
	Detail  string
	Heard   time.Time // when the newest position it ever gave was taken; zero if never
	Source  string    // who gave it
	Misses  int       // asks since it was last heard that found it absent or without a position
}

// RecordPoll notes the outcome of asking about a watch at t. A position (heard non-zero) becomes the newest heard and
// clears the misses; an answer without the aircraft, or without a usable position, adds a miss; a failed ask
// changes neither, since it says nothing about the aircraft.
func (s *Store) RecordPoll(watchID int64, t time.Time, outcome, detail string, heard time.Time, source string) error {
	var h int64
	if !heard.IsZero() {
		h = ts(heard)
	}
	_, err := s.db.Exec(`INSERT INTO poll_state (watch_id, asked_ts, outcome, detail, heard_ts, source, misses)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, CASE WHEN ?3 IN ('not_seen','no_position') THEN 1 ELSE 0 END)
		ON CONFLICT(watch_id) DO UPDATE SET asked_ts=?2, outcome=?3, detail=?4,
			heard_ts = CASE WHEN ?5 > 0 THEN ?5 ELSE heard_ts END,
			source   = CASE WHEN ?5 > 0 THEN ?6 ELSE source END,
			misses   = CASE WHEN ?3 = 'heard' THEN 0 WHEN ?3 IN ('not_seen','no_position') THEN misses + 1 ELSE misses END`,
		watchID, ts(t), outcome, detail, h, source)
	return err
}

// PollStates returns what the last ask found for each watch that has been asked about.
func (s *Store) PollStates() (map[int64]PollState, error) {
	rows, err := s.db.Query(`SELECT watch_id, asked_ts, outcome, detail, heard_ts, source, misses FROM poll_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]PollState{}
	for rows.Next() {
		var id, asked, heard int64
		var p PollState
		if err := rows.Scan(&id, &asked, &p.Outcome, &p.Detail, &heard, &p.Source, &p.Misses); err != nil {
			return nil, err
		}
		p.Asked = time.Unix(asked, 0).UTC()
		if heard > 0 {
			p.Heard = time.Unix(heard, 0).UTC()
		}
		out[id] = p
	}
	return out, rows.Err()
}

// PollStateFor returns what the last ask found for one watch, if it has been asked about.
func (s *Store) PollStateFor(watchID int64) (PollState, bool) {
	all, err := s.PollStates()
	if err != nil {
		return PollState{}, false
	}
	p, ok := all[watchID]
	return p, ok
}
