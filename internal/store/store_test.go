package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
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

// What counts as a sighting of the flight an aircraft is operating: a flight number in a message, a callsign in an AFN logon or on an
// ADS-B position; not a message that names none, one about another aircraft, or a position whose feed had no callsign.
func TestFlightEvidenceSightings(t *testing.T) {
	s := open(t)
	add := func(id int64, hex string, ago time.Duration, flight, callsign, mismatch string) {
		s.AddMessage(Message{ID: id, Hex: hex, T: now.Add(-ago), Label: "H1", Text: "t" + string(rune('a'+id)), Flight: flight, Callsign: callsign, Mismatch: mismatch})
	}
	sightings := func() (out []string) {
		ev, err := s.FlightEvidence("780AB8", now.Add(-24*time.Hour), now)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ev {
			out = append(out, e.ID+"/"+e.Src)
		}
		return
	}
	if got := sightings(); len(got) != 0 {
		t.Fatalf("sightings from nothing: %v", got)
	}
	add(1, "780AB8", 5*time.Hour, "CX833", "", "")
	add(2, "780AB8", 2*time.Hour, "CX113", "", "")
	add(3, "780AB8", time.Hour, "", "", "")                                                            // names no flight
	add(4, "780AB8", 30*time.Minute, "CX999", "", "text names B-LRD, which is hex 780AA4, not 780AB8") // belongs to another aircraft
	add(5, "780AA4", time.Minute, "CX555", "", "")                                                     // another aircraft
	add(6, "780AB8", 10*time.Minute, "", "CPA113", "")                                                 // an AFN logon names only the callsign
	s.AddPosition(Position{Hex: "780AB8", T: now.Add(-3 * time.Hour), Lat: 1, Lon: 1, Callsign: "CPA843", Source: "adsb.lol"})
	s.AddPosition(Position{Hex: "780AB8", T: now.Add(-time.Hour), Lat: 2, Lon: 2, Callsign: "", Source: "adsb.lol"}) // feed had no callsign
	want := []string{"CX833/messages", "CPA843/ADS-B", "CX113/messages", "CPA113/messages"}
	if got := sightings(); !reflect.DeepEqual(got, want) {
		t.Errorf("sightings = %v, want %v", got, want)
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
	// a finished watch is carried on by a new one for the same aircraft, not left beside it
	h.Status = watchlist.StatusDone
	s2.UpdateWatch(h)
	again, _ := s2.AddWatch(tail)
	if again.ID != h.ID || again.Status != watchlist.StatusTracking {
		t.Errorf("a finished watch was not reopened: %+v", again)
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

// An airline's movement message names the route and when the flight left, hours after it did (Qantas sent QF7's
// departure report 11 hours into the flight).
func TestOpsReportsGiveRouteAndDeparture(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 10, 25, 13, 23, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "7C4926", T: at, Label: "80", Text: "a", Kind: "ops_report",
		Decoded: `{"fields":{"ops":{"event":"AD","origin":"YSSY","dest":"KDFW","out":"0213","off":"0233"}}}`})
	s.AddMessage(Message{ID: 2, Hex: "7C4926", T: at.Add(time.Hour), Label: "81", Text: "b", Kind: "ops_report",
		Decoded: `{"fields":{"ops":{"event":"AA","dest":"YSSY","on":"1409"}}}`}) // an arrival names no origin, and no departure

	r, ok := s.LastRoute("7C4926")
	if !ok || r.From != "" || r.To != "YSSY" {
		t.Errorf("newest route = %+v %v (the arrival, which has only a destination)", r, ok)
	}
	rs, err := s.RouteReports("7C4926", at.Add(-time.Hour), at.Add(time.Minute))
	if err != nil || len(rs) != 1 || rs[0].From != "YSSY" || rs[0].To != "KDFW" {
		t.Errorf("route reports = %+v %v", rs, err)
	}
	ds, err := s.Departures("7C4926", at.Add(-24*time.Hour), at.Add(2*time.Hour))
	if err != nil || len(ds) != 2 {
		t.Fatalf("departures = %+v %v", ds, err)
	}
	if ds[0].How != "pushback" || !ds[0].T.Equal(time.Date(2026, 10, 25, 2, 13, 0, 0, time.UTC)) ||
		ds[1].How != "takeoff" || !ds[1].T.Equal(time.Date(2026, 10, 25, 2, 33, 0, 0, time.UTC)) {
		t.Errorf("departures = %+v", ds)
	}
}

// A message about a flight that has not left yet does not name the flight in progress, nor give its route, until the
// flight is due.
func TestMessagesAboutAFlightNotYetDueAreHeldBack(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	sched := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "7C4926", T: at, Label: "B6", Text: "a", Kind: "adsc", Flight: "QF64"})
	s.AddMessage(Message{ID: 2, Hex: "7C4926", T: at.Add(26 * time.Minute), Label: "RF", Text: "b", Kind: "ops_report", Flight: "QF1", Callsign: "QFA1",
		Decoded: `{"fields":{"ops":{"event":"FDACOM","origin":"YSSY","dest":"WSSS","sched":"` + sched.Format(time.RFC3339) + `"}}}`})
	s.AddMessage(Message{ID: 3, Hex: "7C4926", T: at.Add(10 * time.Minute), Label: "81", Text: "c", Kind: "ops_report", Flight: "QF64",
		Decoded: `{"fields":{"ops":{"event":"AA","dest":"YSSY","on":"0310"}}}`})

	if ev, _ := s.FlightEvidence("7C4926", at.Add(-time.Hour), at.Add(time.Hour)); len(ev) != 2 || ev[0].ID != "QF64" || ev[1].ID != "QF64" {
		t.Errorf("sightings by 04:00 = %+v, want only the flight in progress", ev)
	}
	if r, ok := s.LastRouteAt("7C4926", at.Add(time.Hour)); !ok || r.To != "YSSY" {
		t.Errorf("route at 04:00 = %+v %v", r, ok)
	}
	if r, ok := s.LastRouteAt("7C4926", sched.Add(time.Minute)); !ok || r.To != "WSSS" || !r.T.Equal(sched) {
		t.Errorf("route after the departure was due = %+v %v", r, ok)
	}
	ev, _ := s.FlightEvidence("7C4926", at.Add(-time.Hour), at.Add(8*time.Hour))
	if len(ev) != 3 || ev[2].ID != "QF1" || !ev[2].T.Equal(sched) {
		t.Errorf("evidence = %+v (QF1 counts from %v)", ev, sched)
	}
}

// A take-off report with its whole date gives the take-off time exactly, even when it was delivered days late.
func TestExactTakeoffTimeFromALateReport(t *testing.T) {
	s := open(t)
	late := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "A2551D", T: late, Label: "H1", Text: "a", Kind: "ops_report",
		Decoded: `{"fields":{"ops":{"event":"TO","origin":"KDEN","dest":"EGLL","at":"2026-10-26T00:57:43Z","off_at":"2026-10-26T00:57:43Z"}}}`})
	ds, err := s.Departures("A2551D", late.Add(-72*time.Hour), late)
	if err != nil || len(ds) != 1 || ds[0].How != "takeoff" || !ds[0].T.Equal(time.Date(2026, 10, 26, 0, 57, 43, 0, time.UTC)) {
		t.Errorf("departures = %+v %v", ds, err)
	}
}

func TestFlightJoins(t *testing.T) {
	s := open(t)
	a, b := time.Date(2026, 11, 1, 4, 15, 0, 0, time.UTC), time.Date(2026, 11, 1, 12, 46, 0, 0, time.UTC)
	for _, st := range []time.Time{b, a} {
		if err := s.SetFlightJoin("7C4927", st, true, now); err != nil {
			t.Fatal(err)
		}
	}
	s.SetFlightJoin("7C4927", b, true, now) // again: still one
	s.SetFlightJoin("7C4926", a, true, now) // another aircraft
	got, err := s.FlightJoins("7C4927", a.Add(-time.Hour), b.Add(time.Hour))
	if err != nil || len(got) != 2 || !got[0].Equal(a) || !got[1].Equal(b) {
		t.Fatalf("joins = %v %v", got, err)
	}
	if got, _ = s.FlightJoins("7C4927", a.Add(time.Hour), b.Add(time.Hour)); len(got) != 1 {
		t.Errorf("a window: %v", got)
	}
	s.SetFlightJoin("7C4927", a, false, now)
	if got, _ = s.FlightJoins("7C4927", a.Add(-time.Hour), b.Add(time.Hour)); len(got) != 1 || !got[0].Equal(b) {
		t.Errorf("after taking one away: %v", got)
	}
}

func TestHistoryIsNotDoubledAndSearchesAreRemembered(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	t0 := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	st.AddPosition(Position{Hex: "7C4926", T: t0, Lat: 1, Lon: 1, Source: "adsb.lol"})
	var ps []Position
	for _, d := range []int{-30, -5, 4, 12, 40} { // seconds from the stored one
		ps = append(ps, Position{Hex: "7C4926", T: t0.Add(time.Duration(d) * time.Second), Lat: 2 + float64(d), Lon: 2, Source: "adsb.lol-hist"})
	}
	n, err := st.AddHistory("7C4926", ps)
	if err != nil || n != 3 { // -5 and 4 are within HistoryNear of the stored point
		t.Fatalf("wrote %d, %v", n, err)
	}
	if n, _ = st.AddHistory("7C4926", ps); n != 0 {
		t.Errorf("the same history twice wrote %d", n)
	}

	now := time.Now()
	st.MarkBackfill("2026-10-31", "7C4926", 3, now)
	done, _ := st.BackfillScanned("2026-10-31")
	other, _ := st.BackfillScanned("2026-10-30")
	if !done["7C4926"] || len(done) != 1 || len(other) != 0 {
		t.Errorf("%v %v", done, other)
	}
}

func TestProviderHold(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, _, ok := st.ProviderHold("aerodatabox", now); ok {
		t.Error("a hold from nowhere")
	}
	st.SetProviderHold("aerodatabox", now.Add(time.Hour), "quota")
	until, why, ok := st.ProviderHold("aerodatabox", now)
	if !ok || !until.Equal(now.Add(time.Hour)) || why != "quota" {
		t.Errorf("%v %q %v", until, why, ok)
	}
	if _, _, ok := st.ProviderHold("aerodatabox", now.Add(time.Hour)); ok {
		t.Error("still held when the time is up")
	}
	if _, _, ok := st.ProviderHold("other", now); ok {
		t.Error("another service held")
	}
}

// An A350 report counts from the time the aircraft put in it, not from when it arrived; one with no time (the zero time) from
// when it arrived. The window finds a report by either time, and the report says both.
func TestA350RouteReportsCountFromTheirOwnTime(t *testing.T) {
	s := open(t)
	rx := time.Date(2026, 10, 29, 2, 21, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "780AA4", T: rx, Label: "H1", Text: "a", Kind: "miam",
		Decoded: `{"fields":{"rtr":{"Time":"2026-10-27T04:53:20Z","Origin":"VHHH","Dest":"EDDM"}}}`})
	s.AddMessage(Message{ID: 2, Hex: "780AA4", T: rx.Add(time.Minute), Label: "H1", Text: "b", Kind: "miam",
		Decoded: `{"fields":{"a350":{"Time":"0001-01-01T00:00:00Z","Origin":"KSEA","Dest":"VHHH"}}}`})

	// looked for where it happened: found, and said to have arrived two days after
	rs, err := s.RouteReports("780AA4", time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC), time.Date(2026, 10, 27, 5, 30, 0, 0, time.UTC))
	if err != nil || len(rs) != 1 || rs[0].To != "EDDM" || !rs[0].T.Equal(time.Date(2026, 10, 27, 4, 53, 20, 0, time.UTC)) || !rs[0].Rx.Equal(rx) {
		t.Fatalf("%+v %v", rs, err)
	}
	// looked for where it arrived: found too, for the flights code to decide whose it is; the one with no time counts from arrival
	rs, _ = s.RouteReports("780AA4", rx.Add(-time.Hour), rx.Add(time.Hour))
	if len(rs) != 2 || rs[0].To != "EDDM" || rs[1].To != "VHHH" || !rs[1].T.Equal(rx.Add(time.Minute)) {
		t.Errorf("%+v", rs)
	}
}

// A satellite position report (SB..._N77019  KSFOZBAA...) names the flight's origin and destination, and is route evidence like
// the other position reports.
func TestSatellitePositionReportsNameTheRoute(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 11, 1, 19, 30, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "AA6BA4", T: at.Add(100 * time.Second), PosT: &at, Label: "H1", Text: "SB0122BA_N77019  KSFOZBAA777", Kind: "sbpos",
		Decoded: `{"fields":{"origin":"KSFO","dest":"ZBAA","reg":"N77019"}}`})
	rs, err := s.RouteReports("AA6BA4", at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil || len(rs) != 1 || rs[0].From != "KSFO" || rs[0].To != "ZBAA" || !rs[0].T.Equal(at) {
		t.Fatalf("%+v %v", rs, err)
	}
}

// An airline position report delivered a day late has only a time of day for its position, so which day it was sent on is a guess:
// it is not evidence of the route of the flight that guess lands in. One that arrives promptly, or gives its date, is.
func TestALateAirlineReportWithOnlyATimeOfDayIsNotRouteEvidence(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 10, 29, 19, 22, 0, 0, time.UTC)
	ops := `{"fields":{"ops":{"origin":"KSEA","dest":"KMCO"}}}`
	s.AddMessage(Message{ID: 1, Hex: "AD4AFB", T: at.Add(15 * time.Hour), PosT: &at, Label: "H1", Text: "late", Kind: "ops_report", Decoded: ops})
	s.AddMessage(Message{ID: 2, Hex: "AD4AFB", T: at.Add(time.Minute), PosT: &at, Label: "H1", Text: "prompt", Kind: "ops_report", Decoded: ops})
	rs, err := s.RouteReports("AD4AFB", at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil || len(rs) != 1 || rs[0].To != "KMCO" {
		t.Fatalf("%+v %v", rs, err)
	}
}

// An FMS position report that carries the plan (RI:DA:KMCO:AA:KSEA) and the last page of the forecast (which names the airport it
// lands at) are route evidence; the pages before it, with no arrival, and an ordinary progress report are not.
func TestFMSPlanReportsNameTheRoute(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 10, 30, 16, 20, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "AD4AFB", T: at, Label: "H1", Text: "POSN", Kind: "fms_position", Decoded: `{"fields":{"plan":{"from":"KMCO","to":"KSEA"}}}`})
	s.AddMessage(Message{ID: 2, Hex: "AD4AFB", T: at.Add(time.Second), Label: "H1", Text: "a", Kind: "progress", Decoded: `{"fields":{"fms_page":2}}`})
	s.AddMessage(Message{ID: 3, Hex: "AD4AFB", T: at.Add(2 * time.Second), Label: "H1", Text: "b", Kind: "progress", Decoded: `{"fields":{"fms_page":3,"progress":{"to":"KSEA","runway":"16R"}}}`})
	s.AddMessage(Message{ID: 4, Hex: "AD4AFB", T: at.Add(3 * time.Second), Label: "H1", Text: "c", Kind: "progress", Decoded: `{"fields":{"progress":{"to":"KORD"}}}`})
	rs, err := s.RouteReports("AD4AFB", at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil || len(rs) != 2 || rs[0].From != "KMCO" || rs[0].To != "KSEA" || rs[1].From != "" || rs[1].To != "KSEA" {
		t.Fatalf("%+v %v", rs, err)
	}
}

// A message counts as the time the aircraft put in it, where that is a full date and time, not as the time it arrived: an A350
// report delivered two days late is evidence of its flight at the time it was sent, and is listed with that flight.
func TestLateMessagesCountFromTheirOwnTime(t *testing.T) {
	s := open(t)
	sent := time.Date(2026, 10, 27, 5, 0, 0, 0, time.UTC)
	rx := sent.Add(45 * time.Hour)
	// an A350 report (its own time), a REP081 (its position time is when it was made), and an ordinary message with no time of its own
	s.AddMessage(Message{ID: 1, Hex: "780AA4", T: rx, Label: "H1", Text: "a", Kind: "miam", Flight: "CX301",
		Decoded: `{"fields":{"a350":{"Time":"2026-10-27T05:00:00Z","Origin":"VHHH","Dest":"EDDM"}}}`})
	p := sent.Add(time.Minute)
	s.AddMessage(Message{ID: 2, Hex: "780AA4", T: rx.Add(time.Minute), Label: "H1", Text: "b", Kind: "miam", Flight: "CX301", PosT: &p,
		Decoded: `{"fields":{"rep081":{"Origin":"VHHH","Dest":"EDDM"}}}`})
	s.AddMessage(Message{ID: 3, Hex: "780AA4", T: rx.Add(2 * time.Minute), Label: "B9", Text: "c", Kind: "datis_request"})
	// an A350 report with no time of its own counts from its arrival
	s.AddMessage(Message{ID: 4, Hex: "780AA4", T: rx.Add(3 * time.Minute), Label: "H1", Text: "d", Kind: "miam", Flight: "CX833",
		Decoded: `{"fields":{"a350":{"Time":"0001-01-01T00:00:00Z"}}}`})

	ev, err := s.FlightEvidence("780AA4", sent.Add(-time.Hour), rx.Add(time.Hour))
	if err != nil || len(ev) != 3 || !ev[0].T.Equal(sent) || ev[0].ID != "CX301" || !ev[1].T.Equal(p) || !ev[2].T.Equal(rx.Add(3*time.Minute)) || ev[2].ID != "CX833" {
		t.Fatalf("evidence = %+v %v", ev, err)
	}

	// listed with the flight it was sent in, saying when it arrived
	ms, err := s.Messages("780AA4", sent.Add(-time.Hour), sent.Add(time.Hour), 100)
	if err != nil || len(ms) != 2 || ms[0].ID != 1 || !ms[0].At.Equal(sent) || !ms[0].T.Equal(rx) || ms[1].ID != 2 {
		t.Fatalf("messages in the flight's window = %+v %v", ms, err)
	}
	// ... and not with the flight it arrived in
	ms, _ = s.Messages("780AA4", rx.Add(-time.Hour), rx.Add(time.Hour), 100)
	if len(ms) != 2 || ms[0].ID != 3 || ms[1].ID != 4 || !ms[0].At.Equal(ms[0].T) {
		t.Errorf("messages in the window it arrived in = %+v", ms)
	}
}

// An operations event that gives a day of the month and a time of day ("27", "11:58Z"), and an ACMF report with a dated position,
// count from that time, however late they arrive: United's UA26 messages from the 27th, delivered by a VHF station two days later,
// are not evidence of the flight in progress on the 29th.
func TestOpsDaysAndDatedReportsCountFromTheirOwnTime(t *testing.T) {
	s := open(t)
	rx := time.Date(2026, 10, 29, 17, 38, 50, 0, time.UTC)
	s.AddMessage(Message{ID: 1, Hex: "A2551D", T: rx, Label: "49", Text: "a", Kind: "ops_report", Flight: "UA26",
		Decoded: `{"fields":{"ops":{"event":"ICCL","flight":"UA26","info":{"day":"27","time":"11:58Z"}}}}`})
	p := time.Date(2026, 10, 27, 12, 12, 0, 0, time.UTC)
	s.AddMessage(Message{ID: 2, Hex: "A2551D", T: rx.Add(time.Minute), Label: "H1", Text: "b", Kind: "acmf_report", Callsign: "UAL26", PosT: &p,
		Decoded: `{"fields":{"origin":"EGLL","dest":"KDEN"}}`})
	// a month on: a day later in the month than the arrival can only be the month before
	s.AddMessage(Message{ID: 3, Hex: "A2551D", T: time.Date(2026, 11, 1, 0, 30, 0, 0, time.UTC), Label: "49", Text: "c", Kind: "ops_report", Flight: "UA27",
		Decoded: `{"fields":{"ops":{"event":"ICCL","flight":"UA27","info":{"day":"31","time":"23:50Z"}}}}`})
	// a time of day alone (no day) and an ordinary message count from arrival
	s.AddMessage(Message{ID: 4, Hex: "A2551D", T: rx.Add(2 * time.Minute), Label: "49", Text: "d", Kind: "ops_report", Flight: "UA26",
		Decoded: `{"fields":{"ops":{"event":"WXRQ","flight":"UA26","info":{"day":"27"}}}}`})

	ev, err := s.FlightEvidence("A2551D", time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	want := []time.Time{time.Date(2026, 10, 27, 11, 58, 0, 0, time.UTC), p, time.Date(2026, 10, 29, 17, 40, 50, 0, time.UTC), time.Date(2026, 10, 31, 23, 50, 0, 0, time.UTC)}
	if err != nil || len(ev) != len(want) {
		t.Fatalf("%+v %v", ev, err)
	}
	for i, w := range want {
		if !ev[i].T.Equal(w) {
			t.Errorf("sighting %d at %v, want %v", i, ev[i].T, w)
		}
	}
}

func TestStationStatsAndLateness(t *testing.T) {
	s := open(t)
	sent := time.Date(2026, 10, 27, 5, 0, 0, 0, time.UTC)
	id := int64(0)
	add := func(station, srcType, hex string, rx time.Time, kind, decoded string, posT *time.Time) {
		id++
		s.AddMessage(Message{ID: id, Hex: hex, T: rx, Label: "H1", Text: fmt.Sprint("t", id), Kind: kind, Decoded: decoded, Station: station, SourceType: srcType, PosT: posT})
	}
	a350 := func(at time.Time) string {
		return `{"fields":{"a350":{"Time":"` + at.Format("2006-01-02T15:04:05Z") + `","Origin":"VHHH","Dest":"EDDM"}}}`
	}
	// a prompt station: ten A350 reports a few seconds after they were made
	for i := 0; i < 10; i++ {
		at := sent.Add(time.Duration(i) * time.Minute)
		add("PROMPT-1", "vdl", "780AA4", at.Add(5*time.Second), "miam", a350(at), nil)
	}
	// a station that is never prompt: every message 10 to 28 hours late, and one with a clock a little behind the aircraft's
	for i := 0; i < 10; i++ {
		at := sent.Add(time.Duration(i) * time.Minute)
		add("LATE-1", "vdl", "780AA4", at.Add(time.Duration(10+2*i)*time.Hour), "miam", a350(at), nil)
	}
	add("LATE-1", "vdl", "780AB8", sent.Add(-time.Minute), "miam", a350(sent), nil) // stamped before the time inside it: counts as no lag
	// a station with nothing that carries the aircraft's time
	add("QUIET-1", "aero-acars", "780AA4", sent, "datis_request", "", nil)
	add("QUIET-1", "aero-acars", "780AB8", sent.Add(time.Minute), "cpdlc", "", nil)
	s.SaveStations([]StationInfo{{Ident: "LATE-1", Lat: f64(48.1), Lon: f64(17.1), Fuzzed: true, CountryCode: "SK", SourceType: "vdl", Application: "dumpvdl2",
		Flagged: true, FlaggedReason: "Rate anomaly detected", MessagesCount: 235660243, NearestAirport: "LZIB", LastReportAt: sent}}, sent.Add(time.Hour))

	sts, err := s.Stations()
	if err != nil || len(sts) != 3 {
		t.Fatalf("%+v %v", sts, err)
	}
	by := map[string]StationStats{}
	for _, st := range sts {
		by[st.Ident] = st
	}
	p := by["PROMPT-1"]
	if p.Messages != 10 || p.Aircraft != 1 || p.Lateness.Timed != 10 || p.Lateness.Late != 0 || p.Lateness.Shortest != 5*time.Second || p.Lateness.Median != 5*time.Second || p.Info != nil {
		t.Errorf("prompt: %+v", p)
	}
	l := by["LATE-1"]
	if l.Messages != 11 || l.Aircraft != 2 || l.Lateness.Timed != 11 || l.Lateness.Late != 10 || l.Lateness.Shortest != 0 || l.Lateness.P90 != 26*time.Hour || l.Transports["vdl"] != 11 {
		t.Errorf("late: %+v", l)
	}
	if l.Info == nil || !l.Info.Flagged || l.Info.FlaggedReason != "Rate anomaly detected" || *l.Info.Lat != 48.1 || !l.Info.Fuzzed || l.Info.MessagesCount != 235660243 || !l.Info.LastReportAt.Equal(sent) {
		t.Errorf("late info: %+v", l.Info)
	}
	q := by["QUIET-1"]
	if q.Messages != 2 || q.Lateness.Timed != 0 || q.Transports["aero-acars"] != 2 {
		t.Errorf("quiet: %+v", q)
	}
	if got := s.StationsFetched(); !got.Equal(sent.Add(time.Hour)) {
		t.Errorf("fetched %v", got)
	}
	if sts[0].Ident != "LATE-1" {
		t.Errorf("busiest first: %s", sts[0].Ident)
	}
}

func f64(v float64) *float64 { return &v }

func TestWatchingATailAgainReopensItsFinishedWatch(t *testing.T) {
	s := open(t)
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM"})
	first, _ := watchlist.Parse("B-LRM", now.Add(-48*time.Hour), time.Time{})
	a, _ := s.AddWatch(first)
	a.Hex, a.Status, a.Airborne, a.LandedAt = "780AB8", watchlist.StatusDone, true, now.Add(-time.Hour)
	if err := s.UpdateWatch(a); err != nil {
		t.Fatal(err)
	}
	// by registration, by address, and by registration with the address along
	for _, in := range []string{"B-LRM", "hex:780AB8", "tail:B-LRM"} {
		e, _ := watchlist.Parse(in, now, time.Time{})
		got, err := s.AddWatch(e)
		if err != nil || got.ID != a.ID || got.Status != watchlist.StatusTracking || !got.End.Equal(e.End) || !got.Start.Equal(now) || got.Hex != "780AB8" || got.Airborne || !got.LandedAt.IsZero() {
			t.Errorf("%s: %+v %v", in, got, err)
		}
		got.Status = watchlist.StatusDone // finished again, for the next
		s.UpdateWatch(got)
	}
	if all, _ := s.ListWatches(false); len(all) != 1 {
		t.Errorf("one row for the aircraft, got %d: %+v", len(all), all)
	}
	// an open watch is still the one a second add finds, and a finished flight watch is not reopened
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	b, _ := s.AddWatch(e)
	again, _ := s.AddWatch(e)
	if again.ID != b.ID {
		t.Errorf("%d vs %d", again.ID, b.ID)
	}
	f, _ := watchlist.Parse("CPA113", now, time.Time{})
	fa, _ := s.AddWatch(f)
	fa.Status = watchlist.StatusExpired
	s.UpdateWatch(fa)
	fb, _ := s.AddWatch(f)
	if fb.ID == fa.ID || fb.Status != watchlist.StatusWaiting {
		t.Errorf("a flight watch that ran out is not reopened: %+v", fb)
	}
	// an aircraft with no known address is matched by its registration
	n, _ := watchlist.Parse("N24993", now.Add(-48*time.Hour), time.Time{})
	na, _ := s.AddWatch(n)
	na.Status = watchlist.StatusDone
	s.UpdateWatch(na)
	n2, _ := watchlist.Parse("N24993", now, time.Time{})
	if got, _ := s.AddWatch(n2); got.ID != na.ID || got.Status != watchlist.StatusTracking {
		t.Errorf("%+v", got)
	}
}

func TestAnAirframeIsWatchedBeforeItsAddressIsLatched(t *testing.T) {
	s := open(t)
	s.RecordAirframe(Airframe{Hex: "780AB8", Tail: "B-LRM"})
	s.RecordAirframe(Airframe{Hex: "780AA4", Tail: "B-LRD"})
	s.RecordAirframe(Airframe{Hex: "7C4927", Tail: "VH-OQH"})
	watched := func() map[string]bool {
		as, err := s.KnownAirframes()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, a := range as {
			out[a.Hex] = a.Watched
		}
		return out
	}
	// a tail typed in the box has no address until the poller finds the aircraft
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	lrm, _ := s.AddWatch(e)
	if lrm.Hex != "" || !watched()["780AB8"] {
		t.Errorf("a new tail watch with no address yet: %+v, watched %v", lrm, watched())
	}
	// paused does not (nothing is polled; Watch resumes it), finished does not, and neither does a different aircraft
	d, _ := watchlist.Parse("hex:780AA4", now, time.Time{})
	lrd, _ := s.AddWatch(d)
	lrd.Status = watchlist.StatusPaused
	s.UpdateWatch(lrd)
	if watched()["780AA4"] {
		t.Error("a paused watch counts as watched")
	}
	lrm.Status = watchlist.StatusDone
	s.UpdateWatch(lrm)
	if w := watched(); w["780AB8"] || w["7C4927"] {
		t.Errorf("finished or unwatched shown as watched: %v", w)
	}
}

func TestAddWatchSaysWhatItDid(t *testing.T) {
	s := open(t)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	a, how, err := s.AddWatchResult(e)
	if err != nil || how != WatchAdded {
		t.Fatalf("%q %v", how, err)
	}
	if b, how, _ := s.AddWatchResult(e); how != WatchExisting || b.ID != a.ID {
		t.Errorf("again: %q", how)
	}
	a.Status = watchlist.StatusDone
	s.UpdateWatch(a)
	if b, how, _ := s.AddWatchResult(e); how != WatchReopened || b.ID != a.ID {
		t.Errorf("after it ran out: %q", how)
	}
}

func TestRequestCountsAndHoldings(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	st.CountRequest(now, "api.airframes.io", 200, false)
	st.CountRequest(now.Add(time.Minute), "api.airframes.io", 200, false)
	st.CountRequest(now, "api.airframes.io", 429, true)
	st.CountRequest(now, "api.adsb.lol", 0, true)
	st.CountRequest(now.AddDate(0, 0, -3), "api.airframes.io", 200, false)
	got, err := st.RequestCounts(now.AddDate(0, 0, -1))
	if err != nil || len(got) != 2 || got[0] != (RequestCount{"2026-11-05", "api.adsb.lol", 1, 1, 0}) || got[1] != (RequestCount{"2026-11-05", "api.airframes.io", 3, 1, 1}) {
		t.Errorf("since yesterday: %+v %v", got, err)
	}
	if all, _ := st.RequestCounts(time.Time{}); len(all) != 3 {
		t.Errorf("all: %+v", all)
	}

	at := now.Add(-2 * time.Hour)
	st.AddMessage(Message{ID: 1, Hex: "AAAAAA", T: at, Label: "H1", Text: "a", Kind: "fms_position", SourceType: "vdl"})
	st.AddMessage(Message{ID: 2, Hex: "AAAAAA", T: at.AddDate(0, 0, -2), Label: "H1", Text: "b", Kind: "unknown", SourceType: "vdl"})
	st.AddMessage(Message{ID: 3, Hex: "BBBBBB", T: at.AddDate(0, 0, -20), Label: "H1", Text: "c", Kind: "cpdlc", SourceType: "aero-acars"})
	st.AddPosition(Position{Hex: "AAAAAA", T: at, Lat: 1, Lon: 2, Source: "adsb.lol"})
	st.AddPosition(Position{Hex: "CCCCCC", T: at.AddDate(0, 0, -1), Lat: 1, Lon: 2, Source: "history"})
	h, err := st.Holdings(now)
	if err != nil {
		t.Fatal(err)
	}
	if h.Messages != 3 || h.Messages24h != 1 || h.Messages7d != 2 || h.Decoded != 2 || h.Undecoded != 1 || h.Positions != 2 || h.Positions24h != 1 || h.Aircraft != 3 {
		t.Errorf("%+v", h)
	}
	if h.MessagesBy["vdl"] != 2 || h.MessagesBy["aero-acars"] != 1 || h.PositionsBy["adsb.lol"] != 1 || h.PositionsBy["history"] != 1 {
		t.Errorf("by link and source: %+v %+v", h.MessagesBy, h.PositionsBy)
	}
	if len(h.Days) != 7 || h.Days[6].Day != "2026-11-05" || h.Days[6].Messages != 1 || h.Days[6].Positions != 1 || h.Days[4].Messages != 1 || h.Days[5].Positions != 1 {
		t.Errorf("days: %+v", h.Days)
	}

	st.SetProviderHold("aerodatabox", now.Add(time.Hour), "quota")
	st.SetProviderHold("rainviewer", now.Add(-time.Hour), "old")
	if holds, _ := st.ProviderHolds(now); len(holds) != 1 || holds["aerodatabox"].Reason != "quota" {
		t.Errorf("holds: %+v", holds)
	}
}

// A database that recorded radar before tiles had a status gets the column on opening, and its tiles count as pictures.
func TestWxTilesWithoutAStatusAreUpgraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wx.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	frame := time.Date(2026, 11, 4, 7, 20, 0, 0, time.UTC)
	if _, err := s.db.Exec(`ALTER TABLE wx_tile DROP COLUMN status`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO wx_tile (source, frame_ts, z, x, y, bytes, fetched) VALUES ('radar', ?, 7, 100, 60, 10, ?)`, frame.Unix(), frame.Unix()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tiles, err := s.WxFrameTiles("radar", frame)
	if err != nil || len(tiles) != 1 || tiles[0].Status != WxPicture {
		t.Errorf("%+v %v", tiles, err)
	}
	if err := s.AddWxTile(WxTile{Source: "radar", Frame: frame, Z: 7, X: 101, Y: 60, Fetched: frame, Status: WxNone}); err != nil {
		t.Fatal(err)
	}
}
