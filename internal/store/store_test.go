package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
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

func TestWatchDestination(t *testing.T) {
	s := open(t)
	e, _ := watchlist.Parse("UA869 2026-10-31", now, time.Time{})
	a, _ := s.AddWatch(e)
	if a.Dest != "" {
		t.Fatalf("dest = %q", a.Dest)
	}
	e.Dest = "KSFO"
	b, err := s.AddWatch(e) // same flight again, now with a destination
	if err != nil || b.ID != a.ID || b.Dest != "KSFO" {
		t.Fatalf("re-add: %+v %v", b, err)
	}
	all, _ := s.ListWatches(false)
	if len(all) != 1 || all[0].Dest != "KSFO" {
		t.Errorf("list = %+v", all)
	}
	if _, ok := s.LastPosition("NOPE"); ok {
		t.Error("LastPosition of unknown aircraft")
	}
}

// Adding "DAL1317" and then "DAL1317 SLC" is one watch, not two. Dateless
// flights start "now", so the start time cannot be part of the identity.
func TestAddWatchMergesOpenDuplicate(t *testing.T) {
	s := open(t)
	first, _ := watchlist.Parse("DAL1317", now, time.Time{})
	a, err := s.AddWatch(first)
	if err != nil || a.Dest != "" {
		t.Fatalf("first: %+v %v", a, err)
	}
	later := now.Add(7 * time.Minute)
	second, _ := watchlist.Parse("DAL1317 SLC", later, time.Time{})
	b, err := s.AddWatch(second)
	if err != nil || b.ID != a.ID || b.Dest != "SLC" {
		t.Fatalf("second: %+v %v", b, err)
	}
	if !b.End.Equal(later.Add(36 * time.Hour)) { // the later end wins
		t.Errorf("end = %v, want %v", b.End, later.Add(36*time.Hour))
	}
	if all, _ := s.ListWatches(false); len(all) != 1 {
		t.Fatalf("%d entries, want 1: %+v", len(all), all)
	}
	// Adding it again without a destination keeps the one it has, and never shortens the window.
	third, _ := watchlist.Parse("DAL1317", now.Add(time.Minute), now.Add(2*time.Hour))
	c, _ := s.AddWatch(third)
	if c.ID != a.ID || c.Dest != "SLC" || !c.End.Equal(later.Add(36*time.Hour)) {
		t.Errorf("third: %+v", c)
	}

	// A tail added twice is also one watch; an explicit later end extends it.
	t1, _ := watchlist.Parse("B-LRM", now, time.Time{})
	x, _ := s.AddWatch(t1)
	t2, _ := watchlist.Parse("B-LRM", now, now.Add(100*time.Hour))
	y, _ := s.AddWatch(t2)
	if y.ID != x.ID || !y.End.Equal(now.Add(100*time.Hour)) {
		t.Errorf("tail: %+v / %+v", x, y)
	}

	// Different flights, or the same flight on another date, stay separate.
	other, _ := watchlist.Parse("DAL1318", now, time.Time{})
	o, _ := s.AddWatch(other)
	dated1, _ := watchlist.Parse("DAL1317 2026-11-02", now, time.Time{})
	d1, _ := s.AddWatch(dated1)
	if o.ID == a.ID || d1.ID == a.ID {
		t.Errorf("distinct watches merged: %d %d vs %d", o.ID, d1.ID, a.ID)
	}

	// Once finished, watching the same flight again makes a fresh entry.
	a.Status = watchlist.StatusDone
	s.UpdateWatch(a)
	again, _ := watchlist.Parse("DAL1317", now.Add(time.Hour), time.Time{})
	n, _ := s.AddWatch(again)
	if n.ID == a.ID || n.Status != watchlist.StatusWaiting {
		t.Errorf("finished entry was reused: %+v", n)
	}
}

func TestLastRoute(t *testing.T) {
	s := open(t)
	rep := func(id int64, hex string, at time.Time, kind, from, to string) {
		dec := `{"fields":{"rep081":{"Origin":"` + from + `","Dest":"` + to + `"}}}`
		s.AddMessage(Message{ID: id, Hex: hex, T: at, Label: "MA", Text: "t" + string(rune('a'+id)), Kind: kind, Decoded: dec, PosT: &at})
	}
	if _, ok := s.LastRoute("780AB8"); ok {
		t.Fatal("route from nothing")
	}
	rep(1, "780AB8", now.Add(-3*time.Hour), "miam", "VHHH", "YSSY")
	rep(2, "780AB8", now.Add(-time.Hour), "miam", "YSSY", "VHHH")                                                       // newer: the aircraft has turned round
	rep(3, "780AB8", now.Add(-10*time.Minute), "cpdlc", "ZZZZ", "ZZZZ")                                                 // not a REP081, ignored
	rep(4, "780AA4", now, "miam", "VHHH", "EDDM")                                                                       // another aircraft
	s.AddMessage(Message{ID: 5, Hex: "780AB8", T: now, Label: "MA", Text: "x", Kind: "miam", Decoded: `{"fields":{}}`}) // MIAM without a route
	s.AddMessage(Message{ID: 6, Hex: "780AB8", T: now, Label: "MA", Text: "y", Kind: "miam", Decoded: `{"fields":{"rep081":{"Origin":"VHHH","Dest":""}}}`})

	r, ok := s.LastRoute("780AB8")
	if !ok || r.From != "YSSY" || r.To != "VHHH" || !r.T.Equal(now.Add(-time.Hour)) {
		t.Errorf("route = %+v %v", r, ok)
	}
	if r, ok = s.LastRoute("780AA4"); !ok || r.To != "EDDM" {
		t.Errorf("other aircraft = %+v %v", r, ok)
	}
}

func TestLastFlightAndCallsign(t *testing.T) {
	s := open(t)
	add := func(id int64, hex string, ago time.Duration, flight, callsign, mismatch string) {
		s.AddMessage(Message{ID: id, Hex: hex, T: now.Add(-ago), Label: "H1", Text: "t" + string(rune('a'+id)), Flight: flight, Callsign: callsign, Mismatch: mismatch})
	}
	if _, ok := s.LastFlight("780AB8"); ok {
		t.Fatal("flight from nothing")
	}
	add(1, "780AB8", 5*time.Hour, "CX833", "", "")
	add(2, "780AB8", 2*time.Hour, "CX113", "", "")
	add(3, "780AB8", time.Hour, "", "", "")                                                            // names no flight
	add(4, "780AB8", 30*time.Minute, "CX999", "", "text names B-LRD, which is hex 780AA4, not 780AB8") // belongs to another aircraft
	add(5, "780AA4", time.Minute, "CX555", "", "")                                                     // another aircraft
	f, ok := s.LastFlight("780AB8")
	if !ok || f.Flight != "CX113" || !f.T.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("flight = %+v %v", f, ok)
	}
	add(6, "780AB8", 10*time.Minute, "", "CPA113", "") // an AFN logon names only the callsign
	if f, _ = s.LastFlight("780AB8"); f.Callsign != "CPA113" || f.Flight != "" {
		t.Errorf("callsign-only message = %+v", f)
	}

	if _, _, ok := s.LastCallsign("780AB8"); ok {
		t.Fatal("callsign from nothing")
	}
	s.AddPosition(Position{Hex: "780AB8", T: now.Add(-3 * time.Hour), Lat: 1, Lon: 1, Callsign: "CPA843", Source: "adsb.lol"})
	s.AddPosition(Position{Hex: "780AB8", T: now.Add(-time.Hour), Lat: 2, Lon: 2, Callsign: "", Source: "adsb.lol"}) // feed had no callsign
	cs, at, ok := s.LastCallsign("780AB8")
	if !ok || cs != "CPA843" || !at.Equal(now.Add(-3*time.Hour)) {
		t.Errorf("callsign = %q %v %v", cs, at, ok)
	}
}

func TestLearnedFixesAcrossTheDateLine(t *testing.T) {
	s := open(t)
	s.AddFix("DLINE", 1, 5, 179.95, "adsc-eta")
	s.AddFix("DLINE", 2, 5.01, -179.95, "adsc-eta") // 0.1 degree away across the date line: the same place
	s.AddFix("DLINE", 3, 5, 120, "adsc-eta")        // a different one entirely
	got := s.LearnedFixes("DLINE")
	if len(got) != 2 || got[0].N != 2 {
		t.Errorf("fixes = %+v, want the two near the date line counted as one", got)
	}
}

func TestFixNameAt(t *testing.T) {
	s := open(t)
	s.AddFix("SABNO", 1, 18.985, 115.845, "adsc-eta")
	s.AddFix("SABNO", 2, 18.985, 115.845, "adsc-eta")
	s.AddFix("OTHER", 3, 18.985, 115.846, "adsc-eta") // a different name at nearly the same spot, with less support
	s.AddFix("DLINE", 4, 5, 179.999, "adsc-eta")
	if n, ok := s.FixNameAt(18.985, 115.845, 0.005); !ok || n != "SABNO" {
		t.Errorf("got %q %v, want the best supported name", n, ok)
	}
	if _, ok := s.FixNameAt(18.5, 115.845, 0.005); ok {
		t.Error("found a name where there is none")
	}
	if n, ok := s.FixNameAt(5, -179.999, 0.005); !ok || n != "DLINE" {
		t.Errorf("across the date line: %q %v", n, ok)
	}
}

// Watches filed as registrations that are really callsigns (TZP1, CKK624) are corrected on upgrade.
func TestMigrationFixesCallsignsFiledAsTails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ins := func(kind, ident, status, hex, tail string) {
		if _, err := s.db.Exec(`INSERT INTO watch (kind, ident, start_ts, end_ts, status, hex, tail) VALUES (?,?,?,?,?,?,?)`,
			kind, ident, 1, 2, status, hex, tail); err != nil {
			t.Fatal(err)
		}
	}
	ins("tail", "TZP1", "tracking", "", "TZP1")         // wrong: a callsign
	ins("tail", "CKK624", "tracking", "", "CKK624")     // wrong
	ins("tail", "QXZ9999A", "done", "", "QXZ9999A")     // wrong, and finished: stays finished
	ins("tail", "B-LRM", "tracking", "780AB8", "B-LRM") // a real registration, latched
	ins("tail", "N12345", "tracking", "", "N12345")     // a real one, never seen
	ins("tail", "JA123A", "tracking", "", "JA123A")
	ins("tail", "CKK624X7", "tracking", "", "CKK624X7") // five-digit-ish: not a callsign shape, left alone
	ins("hex", "ABC123", "tracking", "ABC123", "")      // an address that looks like a callsign: untouched
	ins("flight", "CPA113", "waiting", "", "")
	s.db.Exec(`PRAGMA user_version = 6`) // as if this database predates the fix
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, _ := s.ListWatches(false)
	got := map[string]string{}
	for _, e := range all {
		got[e.Ident] = string(e.Kind) + "/" + string(e.Status) + "/" + e.Tail
	}
	want := map[string]string{
		"TZP1": "flight/waiting/", "CKK624": "flight/waiting/", "QXZ9999A": "flight/done/",
		"B-LRM": "tail/tracking/B-LRM", "N12345": "tail/tracking/N12345", "JA123A": "tail/tracking/JA123A",
		"CKK624X7": "tail/tracking/CKK624X7", "ABC123": "hex/tracking/", "CPA113": "flight/waiting/",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	// and it ran once: a second open changes nothing
	s.Close()
	s2, _ := Open(path)
	defer s2.Close()
	if all2, _ := s2.ListWatches(false); len(all2) != len(all) {
		t.Errorf("reopen changed the watchlist: %d -> %d", len(all), len(all2))
	}
}

func TestNotes(t *testing.T) {
	s := open(t)
	t1, t2 := now, now.Add(time.Hour)
	a, err := s.AddNote("780AB8", "  The plane Yen took from SEA to HKG on 2026-10-27  \n", t1)
	if err != nil || a.ID == 0 || a.Text != "The plane Yen took from SEA to HKG on 2026-10-27" || !a.Created.Equal(t1) {
		t.Fatalf("add: %+v %v", a, err)
	}
	b, _ := s.AddNote("780AB8", "Hit a bird on 2026-10-29.\nSecond line.", t2)
	s.AddNote("780AA4", "a different aircraft", t1)

	got, err := s.Notes("780AB8")
	if err != nil || len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("newest first expected: %+v %v", got, err)
	}
	if got[0].Text != "Hit a bird on 2026-10-29.\nSecond line." {
		t.Errorf("line breaks must survive: %q", got[0].Text)
	}

	// editing keeps the creation time and moves the update time
	u, err := s.UpdateNote("780AB8", a.ID, "Corrected: Yen flew it on 2026-10-28", t2.Add(time.Hour))
	if err != nil || u.Text != "Corrected: Yen flew it on 2026-10-28" || !u.Created.Equal(t1) || !u.Updated.Equal(t2.Add(time.Hour)) {
		t.Errorf("update: %+v %v", u, err)
	}
	// a note can only be changed or removed through the airframe it belongs to
	if _, err := s.UpdateNote("780AA4", a.ID, "hijack", t2); !errors.Is(err, ErrNoNote) {
		t.Errorf("update through another airframe: %v", err)
	}
	if err := s.DeleteNote("780AA4", a.ID); !errors.Is(err, ErrNoNote) {
		t.Errorf("delete through another airframe: %v", err)
	}
	if n, _ := s.NoteSummary("780AB8"); n != 2 {
		t.Errorf("count = %d", n)
	}
	if n, latest := s.NoteSummary("780AB8"); n != 2 || latest != "Hit a bird on 2026-10-29.\nSecond line." {
		t.Errorf("summary = %d %q", n, latest)
	}
	// bad input
	for text, want := range map[string]error{"": ErrEmptyNote, "  \n\t ": ErrEmptyNote, strings.Repeat("x", MaxNoteBytes+1): ErrLongNote} {
		if _, err := s.AddNote("780AB8", text, now); !errors.Is(err, want) {
			t.Errorf("add %.12q: %v, want %v", text, err, want)
		}
		if _, err := s.UpdateNote("780AB8", a.ID, text, now); !errors.Is(err, want) {
			t.Errorf("update %.12q: %v, want %v", text, err, want)
		}
	}
	if _, err := s.AddNote("780AB8", strings.Repeat("x", MaxNoteBytes), now); err != nil {
		t.Errorf("a note at the limit: %v", err)
	}
	// delete
	if err := s.DeleteNote("780AB8", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNote("780AB8", a.ID); !errors.Is(err, ErrNoNote) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := s.UpdateNote("780AB8", 99999, "x", now); !errors.Is(err, ErrNoNote) {
		t.Errorf("update of nothing: %v", err)
	}
}

// An airframe with any note is never deleted: the database refuses, whoever asks.
func TestAirframesWithNotesCannotBePurged(t *testing.T) {
	s := open(t)
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "x", LastSeen: now})
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-OLD", Source: "x", LastSeen: now.Add(-1000 * time.Hour)}) // an older registration, same aircraft
	s.RecordAirframe(Airframe{Hex: "780AA4", Tail: "B-LRD", Source: "x", LastSeen: now})
	n, _ := s.AddNote("780AB8", "keep this one", now)

	for _, q := range []string{
		`DELETE FROM airframe WHERE hex='780AB8'`,
		`DELETE FROM airframe WHERE tail='B-OLD'`,            // just one of its registrations
		`DELETE FROM airframe WHERE last_seen < 99999999999`, // a broad purge that would otherwise take everything
		`DELETE FROM airframe`,
	} {
		_, err := s.db.Exec(q)
		if err == nil || !strings.Contains(err.Error(), "never purged") {
			t.Errorf("%s: err = %v, want a refusal", q, err)
		}
	}
	all, _ := s.ListAirframes()
	if len(all) != 3 {
		t.Fatalf("rows survived? %d: %+v", len(all), all)
	}
	// the un-noted airframe can be deleted on its own
	if _, err := s.db.Exec(`DELETE FROM airframe WHERE hex='780AA4'`); err != nil {
		t.Errorf("an airframe without notes: %v", err)
	}
	// seeing it again must keep working (that is an update, not a delete-and-reinsert)
	if err := s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Source: "adsb.lol", LastSeen: now.Add(time.Hour)}); err != nil {
		t.Errorf("recording a sighting of a noted airframe: %v", err)
	}
	// only when its last note is gone is it purgeable again
	s.AddNote("780AB8", "second", now)
	s.DeleteNote("780AB8", n.ID)
	if _, err := s.db.Exec(`DELETE FROM airframe WHERE hex='780AB8'`); err == nil {
		t.Error("still has a note: should be refused")
	}
	ns, _ := s.Notes("780AB8")
	s.DeleteNote("780AB8", ns[0].ID)
	if _, err := s.db.Exec(`DELETE FROM airframe WHERE hex='780AB8'`); err != nil {
		t.Errorf("no notes left: %v", err)
	}
}

// An existing database upgrades to have the table and the protection.
func TestNotesMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`DROP TRIGGER airframe_keeps_notes`)
	s.db.Exec(`DROP TABLE airframe_note`)
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Source: "x", LastSeen: now})
	s.db.Exec(`PRAGMA user_version = 7`) // as if made before notes existed
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AddNote("780AB8", "after the upgrade", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM airframe`); err == nil {
		t.Error("the upgraded database does not protect noted airframes")
	}
	if all, _ := s.ListAirframes(); len(all) != 1 {
		t.Errorf("existing airframes lost: %+v", all)
	}
}

func TestKnownAirframes(t *testing.T) {
	s := open(t)
	// 780AB8 has had two registrations; AAAAAA is only on a watch; BBBBBB only has a note; CCCCCC's watch is over
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-OLD", Type: "", Source: "x", FirstSeen: now.Add(-2000 * time.Hour), LastSeen: now.Add(-1000 * time.Hour)})
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "x", FirstSeen: now.Add(-100 * time.Hour), LastSeen: now})
	s.RecordAirframe(Airframe{Hex: "780AA4", Tail: "B-LRD", Type: "A359", Source: "x", FirstSeen: now.Add(-50 * time.Hour), LastSeen: now.Add(-time.Hour)})
	w1, _ := watchlist.Parse("hex:AAAAAA", now, time.Time{})
	s.AddWatch(w1)
	w2, _ := watchlist.Parse("hex:CCCCCC", now, time.Time{})
	w2, _ = s.AddWatch(w2)
	w2.Status = watchlist.StatusDone
	s.UpdateWatch(w2)
	s.AddNote("BBBBBB", "a note about an aircraft we have never seen", now)
	s.AddNote("780AB8", "first", now)
	s.AddNote("780AB8", "second, the newest", now.Add(time.Minute))
	s.AddMessage(Message{ID: 1, Hex: "780AB8", T: now, Label: "BA", Text: "a"})
	s.AddMessage(Message{ID: 2, Hex: "780AB8", T: now, Label: "BA", Text: "b"})
	s.AddPosition(Position{Hex: "780AB8", T: now, Lat: 1, Lon: 1, Source: "x"})

	all, err := s.KnownAirframes()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]KnownAirframe{}
	var order []string
	for _, a := range all {
		by[a.Hex] = a
		order = append(order, a.Hex)
	}
	if len(all) != 5 {
		t.Fatalf("want 5 airframes, got %d: %v", len(all), order)
	}
	m := by["780AB8"]
	if len(m.Tails) != 2 || m.Tails[0].Tail != "B-LRM" || m.Tails[1].Tail != "B-OLD" || m.Type != "A359" {
		t.Errorf("tails newest first, type from the newest that has one: %+v", m)
	}
	// seen means heard from (ADS-B or datalink), not the older times in the airframe table
	if !m.FirstSeen.Equal(now) || !m.LastSeen.Equal(now) || !m.LastADSB.Equal(now) || !m.LastDatalink.Equal(now) {
		t.Errorf("seen %v .. %v (adsb %v, datalink %v)", m.FirstSeen, m.LastSeen, m.LastADSB, m.LastDatalink)
	}
	if m.Notes != 2 || m.LastNote != "second, the newest" || m.Messages != 2 || m.Positions != 1 || m.Watched {
		t.Errorf("counts: %+v", m)
	}
	if a := by["AAAAAA"]; !a.Watched || len(a.Tails) != 0 || !a.LastSeen.IsZero() {
		t.Errorf("watch-only hex: %+v", a)
	}
	if a := by["CCCCCC"]; a.Watched {
		t.Errorf("a finished watch is not watching: %+v", a)
	}
	if a := by["BBBBBB"]; a.Notes != 1 || a.Watched || a.LastNote == "" {
		t.Errorf("note-only hex: %+v", a)
	}
	if order[0] != "780AB8" || order[1] != "780AA4" {
		t.Errorf("most recently seen first: %v", order)
	}
}

// purgeFixture: three aircraft with data at several ages. 780AB8 has a note; the others do not.
func purgeFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s := open(t)
	cutoff := now.Add(-30 * 24 * time.Hour)
	age := func(days float64) time.Time { return now.Add(-time.Duration(days * 24 * float64(time.Hour))) }
	id := int64(0)
	for _, hex := range []string{"780AB8", "780AA4", "AAAAAA"} {
		for _, d := range []float64{40, 31, 29, 1} { // two old enough to go, two recent
			id++
			s.AddMessage(Message{ID: id, Hex: hex, T: age(d), Label: "BA", Text: fmt.Sprint("m", id)})
			s.AddPosition(Position{Hex: hex, T: age(d), Lat: float64(id), Lon: 1, Source: "x"})
		}
		id++
		s.AddMessage(Message{ID: id, Hex: hex, T: cutoff, Label: "BA", Text: fmt.Sprint("m", id)}) // exactly on the cutoff stays
		s.AddPosition(Position{Hex: hex, T: cutoff, Lat: float64(id), Lon: 2, Source: "x"})
	}
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM", Source: "x", LastSeen: age(1)})
	s.RecordAirframe(Airframe{Hex: "780AA4", Tail: "B-LRD", Source: "x", LastSeen: age(1)})
	s.AddNote("780AB8", "Yen's plane", age(35))
	s.AddFix("OLGAN", 1, -5.2, 140.8, "adsc-eta")
	s.SetFetchedTo("780AA4", age(1))

	watch := func(in string, end time.Time, status watchlist.Status, hex string) {
		e, _ := watchlist.Parse(in, age(50), time.Time{})
		e.End, e.Status, e.Hex = end, status, hex
		if e.Kind == watchlist.KindTail {
			e.Tail = e.Ident
		}
		s.AddWatch(e)
	}
	watch("hex:780AA4", age(40), watchlist.StatusDone, "780AA4")     // old, finished, no notes: goes
	watch("flight:CK624", age(45), watchlist.StatusExpired, "")      // old, expired, never found an aircraft: goes
	watch("hex:780AB8", age(40), watchlist.StatusDone, "780AB8")     // old, but its airframe has a note: stays
	watch("tail:B-LRM", age(40), watchlist.StatusDone, "")           // old, never learned the hex, but B-LRM is the noted airframe: stays
	watch("hex:AAAAAA", age(5), watchlist.StatusDone, "AAAAAA")      // finished recently: stays
	watch("hex:BBBBBB", age(40), watchlist.StatusTracking, "BBBBBB") // old end but still open: not a finished watch, stays
	return s, cutoff
}

func TestPurge(t *testing.T) {
	counts := func(s *Store) (pos, msg, watches int) {
		s.db.QueryRow(`SELECT COUNT(*) FROM position`).Scan(&pos)
		s.db.QueryRow(`SELECT COUNT(*) FROM message`).Scan(&msg)
		s.db.QueryRow(`SELECT COUNT(*) FROM watch`).Scan(&watches)
		return
	}
	s, cutoff := purgeFixture(t)
	p0, m0, w0 := counts(s)
	if p0 != 15 || m0 != 15 || w0 != 6 {
		t.Fatalf("fixture: %d positions, %d messages, %d watches", p0, m0, w0)
	}

	// a dry run counts and touches nothing
	dry, err := s.Purge(cutoff, true)
	if err != nil {
		t.Fatal(err)
	}
	if p, m, w := counts(s); p != p0 || m != m0 || w != w0 {
		t.Fatalf("a dry run changed the data: %d %d %d", p, m, w)
	}
	// two aircraft without notes, two rows each older than the cutoff (40 and 31 days); the one exactly on it stays
	if !dry.DryRun || dry.Positions != 4 || dry.Messages != 4 || dry.Watches != 2 {
		t.Errorf("dry run = %+v", dry)
	}
	// the noted aircraft's two old rows of each kind are what it kept
	if dry.KeptAirframes != 1 || dry.KeptPositions != 2 || dry.KeptMessages != 2 {
		t.Errorf("kept = %+v", dry)
	}

	real, err := s.Purge(cutoff, false)
	if err != nil {
		t.Fatal(err)
	}
	if real.Positions != dry.Positions || real.Messages != dry.Messages || real.Watches != dry.Watches || real.KeptPositions != dry.KeptPositions {
		t.Errorf("a real run differs from the dry run: %+v vs %+v", real, dry)
	}
	if p, m, w := counts(s); p != 11 || m != 11 || w != 4 {
		t.Errorf("after: %d positions, %d messages, %d watches", p, m, w)
	}
	// the noted aircraft lost nothing; the others kept their recent data and the row on the cutoff
	for hex, want := range map[string]int{"780AB8": 5, "780AA4": 3, "AAAAAA": 3} {
		var m, p int
		s.db.QueryRow(`SELECT COUNT(*) FROM message WHERE hex=?`, hex).Scan(&m)
		s.db.QueryRow(`SELECT COUNT(*) FROM position WHERE hex=?`, hex).Scan(&p)
		if m != want || p != want {
			t.Errorf("%s keeps %d messages, %d positions, want %d", hex, m, p, want)
		}
	}
	// what is never purged
	af, _ := s.ListAirframes()
	if notes, _ := s.Notes("780AB8"); len(af) != 2 || len(notes) != 1 || len(s.LearnedFixes("OLGAN")) != 1 {
		t.Errorf("airframes %d, notes %d, learned fixes %d", len(af), len(notes), len(s.LearnedFixes("OLGAN")))
	}
	if _, ok := s.FetchedTo("780AA4"); !ok {
		t.Error("fetch state was purged: the next fetch would fetch the purged data again")
	}
	left, _ := s.ListWatches(false)
	kept := map[string]bool{}
	for _, e := range left {
		kept[string(e.Kind)+":"+e.Ident] = true
	}
	for _, k := range []string{"hex:780AB8", "tail:B-LRM", "hex:AAAAAA", "hex:BBBBBB"} {
		if !kept[k] {
			t.Errorf("watch %s was purged; kept: %v", k, kept)
		}
	}
	// running it again finds nothing more
	again, _ := s.Purge(cutoff, false)
	if again.Positions+again.Messages+again.Watches != 0 {
		t.Errorf("second purge removed more: %+v", again)
	}
	// a cutoff before everything removes nothing, a far-future one removes everything of the un-noted
	if none, _ := s.Purge(now.Add(-1000*24*time.Hour), true); none.Positions+none.Messages+none.Watches != 0 {
		t.Errorf("an ancient cutoff: %+v", none)
	}
	all, _ := s.Purge(now.Add(24*time.Hour), false)
	var p, m int
	s.db.QueryRow(`SELECT COUNT(*) FROM position WHERE hex != '780AB8'`).Scan(&p)
	s.db.QueryRow(`SELECT COUNT(*) FROM message WHERE hex != '780AB8'`).Scan(&m)
	if p != 0 || m != 0 {
		t.Errorf("purge of everything left %d positions and %d messages of un-noted aircraft: %+v", p, m, all)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM message WHERE hex = '780AB8'`).Scan(&m)
	if m != 5 {
		t.Errorf("the noted aircraft must keep all %d of its messages, has %d", 5, m)
	}
	if !strings.Contains(real.Report(), "Kept, because their airframe has notes: 1 airframe with 2 old positions and 2 old messages.") ||
		!strings.Contains(dry.Report(), "DRY RUN, nothing deleted.") || !strings.Contains(dry.Report(), "would be removed") {
		t.Errorf("reports:\n%s\n%s", dry.Report(), real.Report())
	}
}

func TestUsageAndCompact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	big := strings.Repeat("payload ", 150) // ~1.2 KB, like a decoded message
	for i := 1; i <= 400; i++ {
		s.AddMessage(Message{ID: int64(i), Hex: "AAAAAA", T: now.Add(-time.Duration(i) * time.Hour), Label: "BA", Text: fmt.Sprint(i), Decoded: big, Detail: big})
	}
	s.RecordAirframe(Airframe{Hex: "AAAAAA", Tail: "N1", Source: "x", LastSeen: now})
	s.AddNote("BBBBBB", "a note about an aircraft with no data", now)

	u, err := s.Usage()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]TableUsage{}
	for _, tu := range u.Tables {
		by[tu.Name] = tu
	}
	if by["message"].Rows != 400 || by["message"].Bytes < 400*1000 || by["airframe"].Rows != 1 || by["airframe_note"].Rows != 1 {
		t.Errorf("tables: %+v", u.Tables)
	}
	if u.Tables[0].Name != "message" {
		t.Errorf("biggest first, got %s", u.Tables[0].Name)
	}
	if u.DBBytes < 400*2000 || u.Oldest.IsZero() || !u.Newest.After(u.Oldest) {
		t.Errorf("db %d bytes, %v .. %v", u.DBBytes, u.Oldest, u.Newest)
	}
	if len(u.Aircraft) != 2 || u.Aircraft[0].Hex != "AAAAAA" || u.Aircraft[0].Messages != 400 || u.Aircraft[0].Tail != "N1" || u.Aircraft[1].Notes != 1 {
		t.Errorf("aircraft: %+v", u.Aircraft)
	}
	rep := u.Report()
	for _, want := range []string{"Database: ", "message ", "By aircraft:", "N1 AAAAAA", "(1 note: its data is never purged)"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}

	// purging frees pages inside the file; compacting gives them back to the disk
	res, err := s.Purge(now.Add(-100*time.Hour), false)
	if err != nil || res.Messages != 300 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	mid, _ := s.Usage()
	if mid.FreeBytes < 250*1000 {
		t.Errorf("expected plenty of empty pages after the purge, got %d", mid.FreeBytes)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Usage()
	if after.FreeBytes != 0 || after.DBBytes >= mid.DBBytes/2 {
		t.Errorf("compact: %d -> %d bytes, %d free", mid.DBBytes, after.DBBytes, after.FreeBytes)
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 1048576: "1.0 MB", 5 << 30: "5.0 GB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// A registration and its ICAO address are one aircraft: watching the second after the first is the same watch.
func TestTailAndHexAreOneWatch(t *testing.T) {
	s := open(t)
	s.RecordAirframe(Airframe{Hex: "A762CC", Tail: "N575UP", Source: "x", LastSeen: now})
	tail, _ := watchlist.Parse("N575UP", now, time.Time{})
	a, _ := s.AddWatch(tail)
	hex, _ := watchlist.Parse("hex:a762cc", now, now.Add(72*time.Hour))
	b, err := s.AddWatch(hex)
	if err != nil || b.ID != a.ID || !b.End.Equal(now.Add(72*time.Hour)) {
		t.Fatalf("hex after tail: %+v %v (first %d)", b, err, a.ID)
	}
	// and the other way round, and the same tail again
	s2 := open(t)
	s2.RecordAirframe(Airframe{Hex: "A762CC", Tail: "N575UP", Source: "x", LastSeen: now})
	h, _ := s2.AddWatch(hex)
	t2, _ := s2.AddWatch(tail)
	if t2.ID != h.ID {
		t.Errorf("tail after hex: %d vs %d", t2.ID, h.ID)
	}
	// an unrelated aircraft, an unknown tail, and a flight are separate watches
	other, _ := watchlist.Parse("N999ZZ", now, time.Time{})
	o, _ := s2.AddWatch(other)
	fl, _ := watchlist.Parse("UPS58", now, time.Time{})
	f, _ := s2.AddWatch(fl)
	if o.ID == h.ID || f.ID == h.ID || o.ID == f.ID {
		t.Errorf("distinct watches merged: %d %d %d", h.ID, o.ID, f.ID)
	}
	// a finished watch does not absorb a new one
	h.Status = watchlist.StatusDone
	s2.UpdateWatch(h)
	again, _ := s2.AddWatch(tail)
	if again.ID == h.ID {
		t.Error("reused a finished watch")
	}
}

func TestFixSummaries(t *testing.T) {
	s := open(t)
	t1, t2 := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC), time.Date(2026, 10, 30, 7, 2, 0, 0, time.UTC)
	for id, at := range map[int64]time.Time{1: t2, 2: t1} { // out of order on purpose
		s.AddMessage(Message{ID: id, Hex: "C880A8", T: at, Label: "BA", Text: fmt.Sprint("m", id), Kind: "cpdlc"})
	}
	s.AddFix("WEDES", 1, 30.668, -123.124, "adsc-eta")
	s.AddFix("WEDES", 2, 30.605, -123.18, "adsc-eta")    // a report that matched a neighbouring waypoint
	s.AddFix("WEDES", 3, 30.668, -123.124, "adsc-chain") // its message has been purged: counts, has no time
	s.AddFix("WEDES", 4, 10, 10, "adsc-eta")             // reused elsewhere: a place of its own
	s.AddFix("DLINE", 5, 5, 179.95, "adsc-eta")
	s.AddFix("DLINE", 6, 5, -179.95, "adsc-eta")
	got, err := s.FixSummaries()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]FixSummary{}
	for _, f := range got {
		by[f.Name] = append(by[f.Name], f)
	}
	if len(by["WEDES"]) != 2 {
		t.Fatalf("WEDES = %+v", by["WEDES"])
	}
	w := by["WEDES"][0]
	if w.N != 3 || !w.First.Equal(t1) || !w.Last.Equal(t2) || len(w.Sources) != 2 || w.Lat != 30.668 || w.Lon != -123.124 {
		t.Errorf("WEDES near = %+v", w)
	}
	if o := by["WEDES"][1]; o.N != 1 || !o.First.IsZero() || o.Lat != 10 {
		t.Errorf("WEDES elsewhere = %+v", o)
	}
	// What most reports agree on is the position, not an average pulled off by the odd one out; the
	// resolver is told the same.
	if l := s.LearnedFixes("WEDES"); len(l) != 2 || l[0].N != 3 || l[0].Lat != 30.668 || l[0].Lon != -123.124 {
		t.Errorf("LearnedFixes = %+v", l)
	}
	// Two sightings either side of the date line are one place.
	if d := by["DLINE"]; len(d) != 1 || d[0].N != 2 || math.Abs(math.Abs(d[0].Lon)-180) > 0.06 {
		t.Errorf("DLINE = %+v", d)
	}
}

func TestPausedWatchIsKeptNotPolledNotExpired(t *testing.T) {
	s := open(t)
	e, _ := watchlist.Parse("B-LRD", now, time.Time{})
	e.Status, e.Hex = watchlist.StatusTracking, "780AA4"
	e, _ = s.AddWatch(e)
	s.AddPosition(Position{Hex: "780AA4", T: now, Lat: 1, Lon: 2, Source: "adsb.lol"})
	e.Pause()
	if err := s.UpdateWatch(e); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.ListWatches(true); len(open) != 0 {
		t.Errorf("a paused watch is still polled: %+v", open)
	}
	if all, _ := s.ListWatches(false); len(all) != 1 || all[0].Status != watchlist.StatusPaused {
		t.Errorf("a paused watch is not listed: %+v", all)
	}
	// It does not run out while paused, and its data is untouched.
	if n, _ := s.ExpireWatches(now.Add(30 * 24 * time.Hour)); n != 0 {
		t.Errorf("expired %d paused watches", n)
	}
	if tr, _ := s.Track("780AA4", now.Add(-time.Hour), now.Add(time.Hour)); len(tr) != 1 {
		t.Errorf("track = %v", tr)
	}
	// Not "watched" on the airframes page: nothing is polled for it.
	as, _ := s.KnownAirframes()
	for _, a := range as {
		if a.Hex == "780AA4" && a.Watched {
			t.Error("a paused watch counts as watched")
		}
	}
	// Watching it again (by registration, or by hex: the same aircraft) resumes the one in the list, not a second.
	again, _ := watchlist.Parse("hex:780AA4", now, time.Time{})
	got, err := s.AddWatch(again)
	if err != nil || got.ID != e.ID || got.Status != watchlist.StatusTracking {
		t.Errorf("re-added = %+v %v", got, err)
	}
	if all, _ := s.ListWatches(false); len(all) != 1 {
		t.Errorf("%d watches after re-adding", len(all))
	}
	if g, err := s.GetWatch(e.ID); err != nil || g.Ident != "B-LRD" {
		t.Errorf("GetWatch = %+v %v", g, err)
	}
	if _, err := s.GetWatch(999); err == nil {
		t.Error("GetWatch of a missing id")
	}
}

// A message flagged as delivered late names a flight that is not the current one: UPS80's logon from 28 October,
// arriving on the 31st, is not what the aircraft is flying now.
func TestLateMessagesDoNotNameTheCurrentFlight(t *testing.T) {
	s := open(t)
	t1, t2 := now.Add(-5*time.Hour), now.Add(-10*time.Minute)
	s.AddMessage(Message{ID: 1, Hex: "A762CC", T: t1, Label: "H1", Text: "a", Kind: "afn", Flight: "UP58", Callsign: "UPS58"})
	s.AddMessage(Message{ID: 2, Hex: "A762CC", T: t2, Label: "H1", Text: "b", Kind: "afn", Flight: "UP80", Callsign: "UPS80", TimeFlag: true, TimeNote: "in a late batch: x"})
	if f, ok := s.LastFlight("A762CC"); !ok || f.Callsign != "UPS58" || !f.T.Equal(t1) {
		t.Errorf("LastFlight = %+v %v", f, ok)
	}
	ev, _ := s.FlightEvidence("A762CC", now.Add(-24*time.Hour), now)
	if len(ev) != 1 || ev[0].ID != "UP58" {
		t.Errorf("evidence = %+v", ev)
	}
}

func TestUnverifiedPositionsDoNotMakeAFlight(t *testing.T) {
	s := open(t)
	lat, lon := 39.1, -123.17
	at := now.Add(-time.Hour)
	s.AddMessage(Message{ID: 1, Hex: "A762CC", T: now, Label: "H1", Text: "a", Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &at, TimeFlag: true})
	s.AddMessage(Message{ID: 2, Hex: "A762CC", T: now, Label: "H1", Text: "b", Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &now})
	got, _ := s.PositionTimes("A762CC", now.Add(-3*time.Hour), now.Add(time.Hour))
	if len(got) != 1 || !got[0].Equal(now) {
		t.Errorf("position times = %v", got)
	}
}

func TestManualRoutes(t *testing.T) {
	s := open(t)
	t0 := now.Add(-10 * time.Hour)
	t1 := now.Add(-4 * time.Hour)
	if err := s.SetManualRoute("780AB8", t0, t1, "KSEA", "VHHH", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetManualRoute("780AB8", now.Add(-3*time.Hour), now, "VHHH", "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetManualRoute("A48E3A", t0, t1, "KJFK", "", now); err != nil { // another aircraft
		t.Fatal(err)
	}
	got, err := s.ManualRoutes("780AB8", now.Add(-24*time.Hour), now)
	if err != nil || len(got) != 2 || got[0].From != "KSEA" || got[0].To != "VHHH" || got[1].From != "VHHH" || got[1].To != "" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, _ := s.ManualRoutes("780AB8", now.Add(-24*time.Hour), now.Add(-8*time.Hour)); len(got) != 1 {
		t.Errorf("only the first overlaps: %+v", got)
	}
	// An entry for a span that overlaps an earlier one replaces it.
	if err := s.SetManualRoute("780AB8", t0.Add(time.Hour), t1.Add(time.Hour), "KSFO", "RJTT", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ManualRoutes("780AB8", now.Add(-24*time.Hour), now.Add(-3*time.Hour-time.Minute)); len(got) != 1 || got[0].From != "KSFO" {
		t.Errorf("replace: %+v", got)
	}
	// Both empty clears it.
	if err := s.SetManualRoute("780AB8", t0, t1, "", "", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ManualRoutes("780AB8", now.Add(-24*time.Hour), now.Add(-3*time.Hour-time.Minute)); len(got) != 0 {
		t.Errorf("clear: %+v", got)
	}
}
