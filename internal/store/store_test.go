package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/watchlist"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWatchRoundTrip(t *testing.T) {
	s := open(t)
	e, _ := watchlist.Parse("UA869 2026-10-31", now, time.Time{})
	a, err := s.AddWatch(e)
	if err != nil || a.ID == 0 {
		t.Fatalf("add: %+v %v", a, err)
	}
	again, _ := s.AddWatch(e)
	if again.ID != a.ID {
		t.Errorf("duplicate add made a new row: %d vs %d", again.ID, a.ID)
	}
	tail, _ := watchlist.Parse("B-LRM", now, time.Time{})
	if _, err := s.AddWatch(tail); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListWatches(false)
	if len(all) != 2 || all[1].Ident != "UA869" || !all[1].End.Equal(e.End) {
		t.Fatalf("list = %+v", all)
	}

	a.Hex, a.Callsign, a.Status = "A1B2C3", "UAL869", watchlist.StatusTracking
	if err := s.UpdateWatch(a); err != nil {
		t.Fatal(err)
	}
	open, _ := s.ListWatches(true)
	if len(open) != 2 {
		t.Fatalf("open = %d", len(open))
	}
	for _, o := range open {
		if o.ID == a.ID && (o.Hex != "A1B2C3" || o.Status != watchlist.StatusTracking) {
			t.Errorf("update lost: %+v", o)
		}
	}
}

func TestExpire(t *testing.T) {
	s := open(t)
	tail, _ := watchlist.Parse("B-LRM", now, time.Time{})           // tracking, ends +36h
	flt, _ := watchlist.Parse("UA869 2026-10-31", now, time.Time{}) // waiting, ends 2026-11-02 00:00
	s.AddWatch(tail)
	s.AddWatch(flt)
	n, err := s.ExpireWatches(now.Add(37 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	n, _ = s.ExpireWatches(now.Add(5 * 24 * time.Hour))
	if n != 1 {
		t.Fatalf("second n=%d", n)
	}
	all, _ := s.ListWatches(false)
	got := map[string]watchlist.Status{}
	for _, e := range all {
		got[e.Ident] = e.Status
	}
	if got["B-LRM"] != watchlist.StatusDone || got["UA869"] != watchlist.StatusExpired {
		t.Errorf("statuses = %v", got)
	}
}

func TestPositionDedupe(t *testing.T) {
	s := open(t)
	alt := 35000.0
	p := Position{Hex: "A1B2C3", T: now, Lat: 40, Lon: -100, AltFt: &alt, Callsign: "UAL869", Source: "adsb.lol"}
	if ok, err := s.AddPosition(p); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, _ := s.AddPosition(p); ok {
		t.Error("same point stored twice")
	}
	same := p
	same.T = now.Add(30 * time.Second) // new time, same place: aircraft stopped or stale data
	if ok, _ := s.AddPosition(same); ok {
		t.Error("unchanged position stored")
	}
	moved := p
	moved.T, moved.Lat = now.Add(60*time.Second), 40.1
	moved.AltFt = nil
	if ok, _ := s.AddPosition(moved); !ok {
		t.Error("moved point not stored")
	}
	tr, err := s.Track("A1B2C3", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(tr) != 2 {
		t.Fatalf("track = %+v %v", tr, err)
	}
	if tr[0].AltFt == nil || *tr[0].AltFt != 35000 || tr[1].AltFt != nil || tr[1].Lat != 40.1 {
		t.Errorf("track = %+v", tr)
	}
}

func TestAirframeTablePersists(t *testing.T) {
	s := open(t)
	t0 := now
	if err := s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "airframes.io", LastSeen: t0}); err != nil {
		t.Fatal(err)
	}
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Source: "adsb.lol", LastSeen: t0.Add(time.Hour)}) // bump, keeps type
	s.RecordAirframe(Airframe{Hex: "", Tail: "X"})                                                            // ignored

	if hex, ok := s.HexForTail("B-LRM"); !ok || hex != "780AB8" {
		t.Errorf("HexForTail = %q %v", hex, ok)
	}
	if tail, ok := s.TailForHex("780AB8"); !ok || tail != "B-LRM" {
		t.Errorf("TailForHex = %q %v", tail, ok)
	}
	// Re-registration: the newest sighting wins, history is kept.
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-NEW", Source: "adsb.lol", LastSeen: t0.Add(48 * time.Hour)})
	if tail, _ := s.TailForHex("780AB8"); tail != "B-NEW" {
		t.Errorf("newest tail = %q", tail)
	}
	all, _ := s.ListAirframes()
	if len(all) != 2 || all[0].Tail != "B-LRM" || all[0].Type != "A359" || !all[0].LastSeen.Equal(t0.Add(time.Hour)) {
		t.Errorf("list = %+v", all)
	}

	// Deleting or expiring watches never touches it.
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	e, _ = s.AddWatch(e)
	s.DeleteWatch(e.ID)
	s.ExpireWatches(now.Add(1000 * time.Hour))
	if _, ok := s.HexForTail("B-LRM"); !ok {
		t.Error("pairing lost with the watch")
	}
}

func TestMigrationFromV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The schema as first shipped: no airborne/landed columns, no airframe table.
	_, err = old.Exec(`CREATE TABLE watch (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, ident TEXT NOT NULL,
		date TEXT NOT NULL DEFAULT '', start_ts INTEGER NOT NULL, end_ts INTEGER NOT NULL, status TEXT NOT NULL,
		hex TEXT NOT NULL DEFAULT '', tail TEXT NOT NULL DEFAULT '', callsign TEXT NOT NULL DEFAULT '',
		UNIQUE (kind, ident, date, start_ts));
		INSERT INTO watch (kind, ident, start_ts, end_ts, status) VALUES ('tail','B-LRM',1,2,'done');`)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.ListWatches(false)
	if err != nil || len(w) != 1 || w[0].Ident != "B-LRM" || w[0].Airborne {
		t.Fatalf("after migrate: %+v %v", w, err)
	}
	if err := s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Source: "x"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s2, err := Open(path); err != nil { // reopening must not re-run migrations
		t.Fatal(err)
	} else {
		s2.Close()
	}
}
