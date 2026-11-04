package store

import (
	"database/sql"
	"time"
)

// WxTile is one recorded weather tile: which source and frame it shows, and where in the tile pyramid.
type WxTile struct {
	Source  string
	Frame   time.Time
	Z, X, Y int
	Bytes   int64
	Fetched time.Time
}

// HaveWxTile reports whether the tile has been recorded.
func (s *Store) HaveWxTile(source string, frame time.Time, z, x, y int) bool {
	var n int
	err := s.db.QueryRow(`SELECT 1 FROM wx_tile WHERE source=? AND frame_ts=? AND z=? AND x=? AND y=?`, source, ts(frame), z, x, y).Scan(&n)
	return err == nil
}

// AddWxTile records a tile; recording one again replaces its row.
func (s *Store) AddWxTile(t WxTile) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO wx_tile (source, frame_ts, z, x, y, bytes, fetched) VALUES (?,?,?,?,?,?,?)`,
		t.Source, ts(t.Frame), t.Z, t.X, t.Y, t.Bytes, ts(t.Fetched))
	return err
}

// WxBytes is how much the recorded tiles weigh.
func (s *Store) WxBytes() (n int64, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM wx_tile`).Scan(&n)
	return
}

// OldestWxFrame returns the earliest frame (of any source) that has recorded tiles.
func (s *Store) OldestWxFrame() (source string, frame time.Time, ok bool) {
	var f int64
	err := s.db.QueryRow(`SELECT source, frame_ts FROM wx_tile ORDER BY frame_ts, source LIMIT 1`).Scan(&source, &f)
	if err == sql.ErrNoRows || err != nil {
		return "", time.Time{}, false
	}
	return source, time.Unix(f, 0).UTC(), true
}

// WxFrameTiles lists the tiles recorded for one frame.
func (s *Store) WxFrameTiles(source string, frame time.Time) ([]WxTile, error) {
	rows, err := s.db.Query(`SELECT z, x, y, bytes, fetched FROM wx_tile WHERE source=? AND frame_ts=? ORDER BY z, y, x`, source, ts(frame))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WxTile
	for rows.Next() {
		t := WxTile{Source: source, Frame: frame}
		var f int64
		if err := rows.Scan(&t.Z, &t.X, &t.Y, &t.Bytes, &f); err != nil {
			return nil, err
		}
		t.Fetched = time.Unix(f, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteWxFrame forgets every tile recorded for one frame.
func (s *Store) DeleteWxFrame(source string, frame time.Time) error {
	_, err := s.db.Exec(`DELETE FROM wx_tile WHERE source=? AND frame_ts=?`, source, ts(frame))
	return err
}

// WxFrame is a recorded frame and how many tiles of it are held.
type WxFrame struct {
	Time  time.Time
	Tiles int
}

// WxFrames lists the frames recorded for a source, oldest first.
func (s *Store) WxFrames(source string) ([]WxFrame, error) {
	rows, err := s.db.Query(`SELECT frame_ts, COUNT(*) FROM wx_tile WHERE source=? GROUP BY frame_ts ORDER BY frame_ts`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WxFrame
	for rows.Next() {
		var f int64
		var n int
		if err := rows.Scan(&f, &n); err != nil {
			return nil, err
		}
		out = append(out, WxFrame{time.Unix(f, 0).UTC(), n})
	}
	return out, rows.Err()
}
