package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// fakeHub stands in for GitHub: release JSON under /repos/..., and the files of src/adsblol under /dl/.
type fakeHub struct {
	*httptest.Server
	src     string
	mu      sync.Mutex
	ranges  []string
	noRange bool                                // answer a Range request with the whole file, as a host that does not support it
	rename  func(name string) string            // changes the asset names the release advertises
	mangle  func(map[string]any) map[string]any // changes the release JSON
}

func newHub(t *testing.T, days ...string) *fakeHub {
	dayPause = 0
	h := &fakeHub{src: t.TempDir()}
	for _, d := range days {
		writeDay(t, h.src, d, map[string][]byte{"./traces/26/trace_full_7c4926.json": gz(traceJSON("VH-OQG"))})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/adsblol/", func(w http.ResponseWriter, r *http.Request) {
		tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		files, _ := filepath.Glob(filepath.Join(h.src, "adsblol", tag+".tar.*"))
		if len(files) == 0 {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		var assets []map[string]any
		for _, f := range files {
			info, _ := os.Stat(f)
			name := filepath.Base(f)
			if h.rename != nil {
				name = h.rename(name)
			}
			assets = append(assets, map[string]any{"name": name, "size": info.Size(), "browser_download_url": h.URL + "/dl/" + filepath.Base(f)})
		}
		rel := map[string]any{"tag_name": tag, "assets": assets}
		if h.mangle != nil {
			rel = h.mangle(rel)
		}
		json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		if rg := r.Header.Get("Range"); rg != "" {
			h.ranges = append(h.ranges, rg)
			if h.noRange {
				r.Header.Del("Range")
			}
		}
		h.mu.Unlock()
		http.ServeFile(w, r, filepath.Join(h.src, "adsblol", filepath.Base(r.URL.Path)))
	})
	h.Server = httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return h
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newFetcher(h *fakeHub) (*Fetcher, *syncBuf) {
	buf := &syncBuf{}
	return &Fetcher{API: h.URL, UserAgent: "test", IdleTimeout: 5 * time.Second,
		Log: slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}, buf
}

func TestDownloadAndImportADay(t *testing.T) {
	hub := newHub(t, "2026-10-30", "2026-10-31")
	f, logs := newFetcher(hub)
	cache := Cache{Dir: t.TempDir()}
	st, _ := store.Open(":memory:")
	defer st.Close()
	st.AddPosition(store.Position{Hex: "7C4926", T: time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC), Lat: 1, Lon: 1, Source: "adsb.lol"})
	m := &Manager{Store: st, Cache: cache, Fetch: f, Log: f.Log, Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }}

	p, err := m.Plan("")
	if err != nil || len(p.Days) != 2 || p.Days[0].Day != "2026-10-31" || p.Days[0].Cached || p.Aircraft != 1 || p.Download != 2*EstDayBytes {
		t.Fatalf("%+v %v", p, err)
	}
	// Nov 1 is not yet published at noon that day (its release comes about 03:30Z the next): it is not planned
	for _, d := range p.Days {
		if d.Day > "2026-10-31" {
			t.Errorf("planned an unpublished day %s", d.Day)
		}
	}
	if err := m.Start(""); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(""); !errors.Is(err, ErrBusy) && m.Status().Running {
		t.Errorf("a second backfill while one runs: %v", err)
	}
	m.Wait()
	s := m.Status()
	if s.Running || s.Error != "" || s.DaysDone != 2 || s.Added != 4 {
		t.Fatalf("%+v", s)
	}
	for _, d := range []string{"2026-10-30", "2026-10-31"} {
		if _, err := cache.Parts(d); err != nil {
			t.Errorf("%s not kept in the cache: %v", d, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(cache.Dir, "adsblol", "*.part")); len(left) != 0 {
		t.Errorf("left partial files %v", left)
	}
	log := logs.String()
	for _, want := range []string{"found the release", "download starts", "part complete", "download complete", "mb_per_s", "positions added"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log never says %q:\n%s", want, log)
		}
	}
	// nothing left to do now
	if err := m.Start(""); err == nil || !strings.Contains(err.Error(), "nothing to do") {
		t.Errorf("%v", err)
	}
}

func TestDownloadResumesAHalfFinishedPart(t *testing.T) {
	hub := newHub(t, "2026-10-31")
	f, logs := newFetcher(hub)
	cache := Cache{Dir: t.TempDir()}
	rel, err := f.Release(context.Background(), "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	first := rel.Assets[0]
	src, _ := os.ReadFile(filepath.Join(hub.src, "adsblol", first.Name))
	os.MkdirAll(filepath.Join(cache.Dir, "adsblol"), 0o755)
	os.WriteFile(filepath.Join(cache.Dir, "adsblol", first.Name+".part"), src[:len(src)/2], 0o644)

	var done, total int64
	if err := f.Download(context.Background(), cache, rel, func(d, t int64) { done, total = d, t }); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(cache.Dir, "adsblol", first.Name))
	if !bytes.Equal(got, src) || done != total || total != rel.Bytes() {
		t.Errorf("resumed file differs, or progress %d/%d", done, total)
	}
	if len(hub.ranges) != 1 || hub.ranges[0] != fmt.Sprintf("bytes=%d-", len(src)/2) || !strings.Contains(logs.String(), "resuming a part") {
		t.Errorf("ranges %v\n%s", hub.ranges, logs.String())
	}

	// a host that ignores Range: the part starts again, and the log says why it matters
	hub.noRange = true
	os.Remove(filepath.Join(cache.Dir, "adsblol", first.Name))
	os.WriteFile(filepath.Join(cache.Dir, "adsblol", first.Name+".part"), src[:10], 0o644)
	if err := f.Download(context.Background(), cache, rel, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = os.ReadFile(filepath.Join(cache.Dir, "adsblol", first.Name)); !bytes.Equal(got, src) {
		t.Error("restarted file differs")
	}
	if !strings.Contains(logs.String(), "ignored the resume request") {
		t.Errorf("no hint that Range stopped working:\n%s", logs.String())
	}
}

func TestWhenTheLayoutChangesTheLogSaysWhatToCheck(t *testing.T) {
	hub := newHub(t, "2026-10-31")
	f, logs := newFetcher(hub)

	// no release for the day
	if _, err := f.Release(context.Background(), "2026-10-29"); !errors.Is(err, ErrNotPublished) {
		t.Errorf("%v", err)
	}
	if l := logs.String(); !strings.Contains(l, "no usable release") || !strings.Contains(l, "03:30Z") || !strings.Contains(l, "github.com/adsblol/globe_history_2026/releases") {
		t.Errorf("%s", l)
	}

	// the assets have been renamed
	hub.rename = func(n string) string { return strings.Replace(n, "planes-readsb", "planes", 1) }
	if _, err := f.Release(context.Background(), "2026-10-31"); err == nil || errors.Is(err, ErrNotPublished) {
		t.Errorf("a release with renamed files is not an unpublished one: %v", err)
	}
	if l := logs.String(); !strings.Contains(l, "no tar parts we recognise") || !strings.Contains(l, "-planes-prod-0.tar.aa") {
		t.Errorf("renamed assets are not reported with their names:\n%s", l)
	}

	// the JSON is not what was expected
	hub.rename = nil
	hub.mangle = func(map[string]any) map[string]any { return map[string]any{"tag_name": 5, "assets": "none"} }
	if _, err := f.Release(context.Background(), "2026-10-31"); err == nil || !strings.Contains(logs.String(), "release API shape has changed") {
		t.Errorf("%v\n%s", err, logs.String())
	}

	// an archive whose files are laid out differently reads as an error, not as "nobody had a trace"
	dir := t.TempDir()
	parts := writeDay(t, dir, "2026-10-31", map[string][]byte{"./flights/26/7c4926.json": gz(traceJSON("VH-OQG"))})
	_, err := Scan(parts, map[string]bool{"7c4926": true}, func(string, io.Reader) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "may have changed the layout") || !strings.Contains(err.Error(), "flights/26/7c4926.json") {
		t.Errorf("%v", err)
	}
}

func TestTheCacheKeepsToItsLimit(t *testing.T) {
	hub := newHub(t, "2026-10-31")
	f, logs := newFetcher(hub)
	cache := Cache{Dir: t.TempDir()}
	// two old days, each 1000 bytes, from before; the new day is a few KB
	for _, d := range []string{"2026-10-01", "2026-10-02"} {
		os.MkdirAll(filepath.Join(cache.Dir, "adsblol"), 0o755)
		os.WriteFile(filepath.Join(cache.Dir, "adsblol", partPrefix(d, "prod")+"aa"), make([]byte, 1000), 0o644)
	}
	rel, _ := f.Release(context.Background(), "2026-10-31")
	st, _ := store.Open(":memory:")
	defer st.Close()
	m := &Manager{Store: st, Cache: cache, Fetch: f, Log: f.Log, MaxBytes: rel.Bytes() + 1500}
	// room for the new day and one old: the oldest goes
	if ev, tooBig := m.evictions(cache.Bytes(), rel.Bytes(), nil); tooBig || len(ev) != 1 || ev[0] != "2026-10-01" {
		t.Errorf("%v %v", ev, tooBig)
	}
	// ... unless a plan still has to read it
	if ev, _ := m.evictions(cache.Bytes(), rel.Bytes(), map[string]bool{"2026-10-01": true}); len(ev) != 1 || ev[0] != "2026-10-02" {
		t.Errorf("%v", ev)
	}
	if _, tooBig := m.evictions(cache.Bytes(), rel.Bytes(), map[string]bool{"2026-10-01": true, "2026-10-02": true}); !tooBig {
		t.Error("everything kept and still too big")
	}
	m.MaxBytes = 100
	if _, tooBig := m.evictions(cache.Bytes(), rel.Bytes(), nil); !tooBig {
		t.Error("a day larger than the whole limit")
	}
	_ = logs
	days, _ := cache.Days()
	if len(days) != 2 || days[0].Bytes != 1000 {
		t.Errorf("%+v", days)
	}
	if freed, err := cache.Remove("2026-10-01"); err != nil || freed != 1000 {
		t.Errorf("%d %v", freed, err)
	}
}
