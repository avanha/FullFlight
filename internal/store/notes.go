package store

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// MaxNoteBytes bounds one note. Enough for a paragraph or three.
const MaxNoteBytes = 4000

var (
	ErrNoNote    = errors.New("no such note")
	ErrEmptyNote = errors.New("a note cannot be empty")
	ErrLongNote  = errors.New("note is too long")
)

// Note is something you wrote about an airframe, e.g. "The plane Yen took from SEA to HKG on 2026-10-27".
type Note struct {
	ID      int64
	Hex     string
	Text    string
	Created time.Time
	Updated time.Time
}

func cleanNote(text string) (string, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", ErrEmptyNote
	case len(text) > MaxNoteBytes:
		return "", ErrLongNote
	}
	return text, nil
}

// AddNote adds a note to an airframe. An airframe with a note is never purged.
func (s *Store) AddNote(hexAddr, text string, now time.Time) (Note, error) {
	text, err := cleanNote(text)
	if err != nil {
		return Note{}, err
	}
	res, err := s.db.Exec(`INSERT INTO airframe_note (hex, note, created, updated) VALUES (?,?,?,?)`, hexAddr, text, ts(now), ts(now))
	if err != nil {
		return Note{}, err
	}
	id, _ := res.LastInsertId()
	return Note{ID: id, Hex: hexAddr, Text: text, Created: now.UTC().Truncate(time.Second), Updated: now.UTC().Truncate(time.Second)}, nil
}

// UpdateNote rewrites a note of the given airframe. A note of another airframe is ErrNoNote.
func (s *Store) UpdateNote(hexAddr string, id int64, text string, now time.Time) (Note, error) {
	text, err := cleanNote(text)
	if err != nil {
		return Note{}, err
	}
	res, err := s.db.Exec(`UPDATE airframe_note SET note=?, updated=? WHERE id=? AND hex=?`, text, ts(now), id, hexAddr)
	if err != nil {
		return Note{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Note{}, ErrNoNote
	}
	return s.note(hexAddr, id)
}

// DeleteNote removes a note. Removing an airframe's last note makes it purgeable again.
func (s *Store) DeleteNote(hexAddr string, id int64) error {
	res, err := s.db.Exec(`DELETE FROM airframe_note WHERE id=? AND hex=?`, id, hexAddr)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoNote
	}
	return nil
}

func (s *Store) note(hexAddr string, id int64) (Note, error) {
	rows, err := s.db.Query(`SELECT id, hex, note, created, updated FROM airframe_note WHERE id=? AND hex=?`, id, hexAddr)
	if err != nil {
		return Note{}, err
	}
	notes, err := scanNotes(rows)
	if err != nil {
		return Note{}, err
	}
	if len(notes) == 0 {
		return Note{}, ErrNoNote
	}
	return notes[0], nil
}

// Notes returns an airframe's notes, newest first.
func (s *Store) Notes(hexAddr string) ([]Note, error) {
	rows, err := s.db.Query(`SELECT id, hex, note, created, updated FROM airframe_note WHERE hex=? ORDER BY created DESC, id DESC`, hexAddr)
	if err != nil {
		return nil, err
	}
	return scanNotes(rows)
}

func scanNotes(rows *sql.Rows) ([]Note, error) {
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var c, u int64
		if err := rows.Scan(&n.ID, &n.Hex, &n.Text, &c, &u); err != nil {
			return nil, err
		}
		n.Created, n.Updated = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoteSummary is how many notes an airframe has and the text of the newest.
func (s *Store) NoteSummary(hexAddr string) (count int, latest string) {
	s.db.QueryRow(`SELECT COUNT(*) FROM airframe_note WHERE hex=?`, hexAddr).Scan(&count)
	if count > 0 {
		s.db.QueryRow(`SELECT note FROM airframe_note WHERE hex=? ORDER BY created DESC, id DESC LIMIT 1`, hexAddr).Scan(&latest)
	}
	return
}

// TailSeen is one registration an airframe has been seen with.
type TailSeen struct {
	Tail      string
	Type      string
	FirstSeen time.Time
	LastSeen  time.Time
}

// KnownAirframe is everything we hold about one aircraft, by its hex address.
type KnownAirframe struct {
	Hex       string
	Tails     []TailSeen // newest first; empty for a hex we only know from a watch or a note
	Type      string     // the newest non-empty type
	FirstSeen time.Time  // zero if no airframe row
	LastSeen  time.Time
	Watched   bool // on an open watch
	Notes     int
	LastNote  string
	Messages  int
	Positions int
}

// KnownAirframes lists every airframe: those recorded from sightings and lookups, and any hex that is on
// a watch or has a note even if we have never recorded its registration.
func (s *Store) KnownAirframes() ([]KnownAirframe, error) {
	byHex := map[string]*KnownAirframe{}
	get := func(h string) *KnownAirframe {
		if byHex[h] == nil {
			byHex[h] = &KnownAirframe{Hex: h}
		}
		return byHex[h]
	}

	rows, err := s.db.Query(`SELECT hex, tail, icao_type, first_seen, last_seen FROM airframe ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h string
		var t TailSeen
		var f, l int64
		if err := rows.Scan(&h, &t.Tail, &t.Type, &f, &l); err != nil {
			rows.Close()
			return nil, err
		}
		t.FirstSeen, t.LastSeen = time.Unix(f, 0).UTC(), time.Unix(l, 0).UTC()
		a := get(h)
		a.Tails = append(a.Tails, t)
		if a.Type == "" {
			a.Type = t.Type // rows come newest first
		}
		if a.FirstSeen.IsZero() || t.FirstSeen.Before(a.FirstSeen) {
			a.FirstSeen = t.FirstSeen
		}
		if t.LastSeen.After(a.LastSeen) {
			a.LastSeen = t.LastSeen
		}
	}
	rows.Close()

	each := func(q string, f func(h string, n int)) error {
		rs, err := s.db.Query(q)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var h string
			var n int
			if err := rs.Scan(&h, &n); err != nil {
				return err
			}
			f(h, n)
		}
		return rs.Err()
	}
	if err := each(`SELECT hex, COUNT(*) FROM watch WHERE hex != '' AND status IN ('waiting','tracking') GROUP BY hex`,
		func(h string, _ int) { get(h).Watched = true }); err != nil {
		return nil, err
	}
	if err := each(`SELECT hex, COUNT(*) FROM airframe_note GROUP BY hex`, func(h string, n int) { get(h).Notes = n }); err != nil {
		return nil, err
	}
	// hexes we have only watched also get listed, whatever their state
	if err := each(`SELECT hex, COUNT(*) FROM watch WHERE hex != '' GROUP BY hex`, func(h string, _ int) { get(h) }); err != nil {
		return nil, err
	}
	if err := each(`SELECT hex, COUNT(*) FROM message GROUP BY hex`, func(h string, n int) {
		if a := byHex[h]; a != nil {
			a.Messages = n
		}
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT hex, COUNT(*) FROM position GROUP BY hex`, func(h string, n int) {
		if a := byHex[h]; a != nil {
			a.Positions = n
		}
	}); err != nil {
		return nil, err
	}
	for _, a := range byHex {
		if a.Notes > 0 {
			_, a.LastNote = s.NoteSummary(a.Hex)
		}
	}

	out := make([]KnownAirframe, 0, len(byHex))
	for _, a := range byHex {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].Hex < out[j].Hex
	})
	return out, nil
}
