package history

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// base is 2026-10-31 00:00:00Z, the day of the real archive these traces are modelled on.
const base = 1793404800.0

// A trace as readsb writes it: a base time and rows of [offset, lat, lon, alt, gs, track, flags, vrate, details, source].
func traceJSON(reg string) string {
	return fmt.Sprintf(`{"icao":"7c4926","r":%q,"t":"A388","timestamp":%v,"trace":[
		[19.62,-35.925991,148.451239,32000,452.7,235.7,0,-64,null,"adsb_icao"],
		[39.61,-35.949873,148.408697,32000,454.1,235.2,0,-64,{"type":"adsb_icao","flight":"QFA63   "},"adsb_icao"],
		[59.14,-35.973475,148.367147,31900,453.3,235.1,1,-128,null,"adsb_icao"],
		[79.14,-36.0,148.3,"ground",12.0,235.1,2,0,null,"adsb_icao"],
		[99.14,-36.1,148.2,31800,450.0,235.0,0,-64,{"flight":"QFA64"},"adsb_icao"]]}`, reg, base)
}

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// writeDay puts a day in dir/adsblol as two parts of one tar, the way it is published: cut anywhere, not at a file.
func writeDay(t *testing.T, dir, day string, files map[string][]byte) []string {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "./heatmap/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "./heatmap/33.bin.ttf", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3})
	tw.Write([]byte("xyz"))
	for name, data := range files {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))})
		tw.Write(data)
	}
	tw.Close()
	all := b.Bytes()
	cut := len(all) / 3
	prefix := "v" + day[:4] + "." + day[5:7] + "." + day[8:] + "-planes-readsb-prod-0.tar."
	os.MkdirAll(filepath.Join(dir, "adsblol"), 0o755)
	var out []string
	for i, part := range [][]byte{all[:cut], all[cut:]} {
		p := filepath.Join(dir, "adsblol", prefix+string(rune('a'))+string(rune('a'+i)))
		if err := os.WriteFile(p, part, 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestParseTrace(t *testing.T) {
	ps, err := ParseTrace(bytes.NewReader(gz(traceJSON("vh-oqg"))))
	if err != nil {
		t.Fatal(err)
	}
	// the stale row (flags 1) is left out; the others are kept
	if len(ps) != 4 {
		t.Fatalf("%d points: %+v", len(ps), ps)
	}
	p := ps[0]
	if want := time.Unix(1793404819, 0).UTC(); !p.T.Equal(want) {
		t.Errorf("time %v want %v", p.T, want)
	}
	if p.Reg != "VH-OQG" || p.Source != Source || p.AltFt == nil || *p.AltFt != 32000 || *p.GS != 452.7 || *p.VRate != -64 || p.Callsign != "" {
		t.Errorf("first point %+v", p)
	}
	if ps[1].Callsign != "QFA63" || ps[2].Callsign != "QFA63" || ps[3].Callsign != "QFA64" {
		t.Errorf("callsigns carried forward: %q %q %q", ps[1].Callsign, ps[2].Callsign, ps[3].Callsign)
	}
	if g := ps[2]; !g.OnGround || g.AltFt != nil {
		t.Errorf("a point on the ground has no altitude and says so: %+v", g)
	}
	if _, err := ParseTrace(bytes.NewReader([]byte("not gzip"))); err == nil {
		t.Error("a damaged file is an error")
	}
}

func TestImportDayReadsTheArchiveOnceForEveryAircraft(t *testing.T) {
	dir := t.TempDir()
	writeDay(t, dir, "2026-10-31", map[string][]byte{
		"./traces/26/trace_full_7c4926.json": gz(traceJSON("VH-OQG")),
		"./traces/e3/trace_full_76cce3.json": gz(traceJSON("9V-SGC")),
		"./traces/00/trace_full_000000.json": gz(traceJSON("X-OTHER")), // an aircraft nobody asked for
	})
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	// one of the positions is already held, a few seconds off the archive's: it is not doubled
	st.AddPosition(store.Position{Hex: "7C4926", T: time.Unix(1793404822, 0).UTC(), Lat: -35.9, Lon: 148.4, Source: "adsb.lol"})

	res, err := ImportDay(st, Cache{Dir: dir}, "2026-10-31", []string{"7C4926", "76CCE3", "ABCDEF"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Searched != 3 || res.Found != 2 || res.Positions["7C4926"] != 3 || res.Positions["76CCE3"] != 4 || res.Total() != 7 {
		t.Fatalf("%+v", res)
	}
	if got, _ := st.PositionTimes("000000", time.Unix(0, 0), now); len(got) != 0 {
		t.Error("an aircraft nobody asked for was stored")
	}

	// the same day for the same aircraft is not read again, even with the archive gone; a new aircraft is searched for
	os.RemoveAll(filepath.Join(dir, "adsblol"))
	res, err = ImportDay(st, Cache{Dir: dir}, "2026-10-31", []string{"7C4926", "ABCDEF"}, now)
	if err != nil || res.Skipped != 2 || res.Searched != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err = ImportDay(st, Cache{Dir: dir}, "2026-10-31", []string{"7C4926", "A00001"}, now); !errors.Is(err, ErrNotCached) {
		t.Errorf("a new aircraft needs the archive, which is gone: %v", err)
	}
}

func TestImportDayNeedsTheWholeArchive(t *testing.T) {
	dir := t.TempDir()
	parts := writeDay(t, dir, "2026-10-31", map[string][]byte{"./traces/26/trace_full_7c4926.json": gz(traceJSON("VH-OQG"))})
	st, _ := store.Open(":memory:")
	defer st.Close()
	// the second part cut short: the day must not count as searched
	b, _ := os.ReadFile(parts[1])
	os.WriteFile(parts[1], b[:len(b)-1100], 0o644)
	if _, err := ImportDay(st, Cache{Dir: dir}, "2026-10-31", []string{"7C4926"}, time.Now()); err == nil {
		t.Fatal("a truncated archive was accepted")
	}
	done, _ := st.BackfillScanned("2026-10-31")
	if len(done) != 0 {
		t.Errorf("marked as searched: %v", done)
	}
	// and a part missing from the middle is named
	os.Remove(parts[0])
	if _, err := (Cache{Dir: dir}).Parts("2026-10-31"); err == nil {
		t.Error("a missing first part was not noticed")
	}
}
