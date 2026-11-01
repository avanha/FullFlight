package history

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

const (
	// EstDayBytes is what an uncached day is taken to weigh before its release has been looked up: adsb.lol's days run
	// 3 to 4 GB.
	EstDayBytes = 3_500_000_000
	// PublishedAfter is how long after a UTC day ends its release can be expected: about 03:30Z, plus an hour's margin.
	PublishedAfter = 28*time.Hour + 30*time.Minute
)

// dayPause is the wait between days, so a long backfill is gentle on GitHub.
var dayPause = 2 * time.Second

// Manager plans and runs backfills, one at a time.
type Manager struct {
	Store    *store.Store
	Cache    Cache
	MaxBytes int64 // the cache's size limit; the oldest days are deleted to make room
	Fetch    *Fetcher
	Log      *slog.Logger
	Now      func() time.Time

	mu  sync.Mutex
	st  Status
	end context.CancelFunc
	wg  sync.WaitGroup
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// PlanDay is one day that has aircraft still to be searched for.
type PlanDay struct {
	Day      string `json:"day"`
	Aircraft int    `json:"aircraft"` // how many aircraft would be looked for
	Cached   bool   `json:"cached"`
	Bytes    int64  `json:"bytes"` // to download: 0 when cached
}

// Plan is what a backfill would do.
type Plan struct {
	Days       []PlanDay `json:"days"`
	Aircraft   int       `json:"aircraft"`
	Download   int64     `json:"download_bytes"`
	CacheBytes int64     `json:"cache_bytes"`
	CacheLimit int64     `json:"cache_limit_bytes"`
	Evict      []string  `json:"evict,omitempty"` // cached days that would be deleted to make room, oldest first
	TooBig     bool      `json:"too_big,omitempty"`

	hexes map[string][]string // day -> upper-case hexes still to search for
}

// Plan works out which days would be read, for hexAddr alone or for every aircraft we hold anything for. For each aircraft,
// those are the days it has positions or messages on and the day either side (a flight crosses midnight, and the point is the
// time before we had positions), which are already published and not yet searched. Cached days come first, then the others
// newest first.
func (m *Manager) Plan(hexAddr string) (Plan, error) {
	hexAddr = strings.ToUpper(hexAddr)
	by, err := m.Store.HexDays(hexAddr)
	if err != nil {
		return Plan{}, err
	}
	last := m.now().UTC().Add(-PublishedAfter).Format("2006-01-02")
	want := map[string]map[string]bool{} // day -> hexes
	for h, days := range by {
		for _, d := range days {
			t, err := time.Parse("2006-01-02", d)
			if err != nil {
				continue
			}
			for _, off := range []int{-1, 0, 1} {
				day := t.AddDate(0, 0, off).Format("2006-01-02")
				if day > last {
					continue
				}
				if want[day] == nil {
					want[day] = map[string]bool{}
				}
				want[day][h] = true
			}
		}
	}
	p := Plan{CacheLimit: m.MaxBytes, hexes: map[string][]string{}}
	seen := map[string]bool{}
	for day, hs := range want {
		done, err := m.Store.BackfillScanned(day)
		if err != nil {
			return Plan{}, err
		}
		var todo []string
		for h := range hs {
			if !done[h] {
				todo = append(todo, h)
			}
		}
		if len(todo) == 0 {
			continue
		}
		sort.Strings(todo)
		pd := PlanDay{Day: day, Aircraft: len(todo)}
		if _, err := m.Cache.Parts(day); err == nil {
			pd.Cached = true
		} else {
			pd.Bytes = EstDayBytes
			p.Download += pd.Bytes
		}
		p.Days = append(p.Days, pd)
		p.hexes[day] = todo
		for _, h := range todo {
			seen[h] = true
		}
	}
	p.Aircraft = len(seen)
	sort.Slice(p.Days, func(i, j int) bool {
		a, b := p.Days[i], p.Days[j]
		if a.Cached != b.Cached {
			return a.Cached
		}
		return a.Day > b.Day
	})
	p.CacheBytes = m.Cache.Bytes()
	keep := map[string]bool{}
	for d := range p.hexes {
		keep[d] = true
	}
	p.Evict, p.TooBig = m.evictions(p.CacheBytes, p.Download, keep)
	return p, nil
}

// evictions picks the cached days that have to go to fit need more bytes in a cache holding have, oldest first, never one in
// keep (the days still to be read). tooBig says that even deleting all of them would not make room.
func (m *Manager) evictions(have, need int64, keep map[string]bool) (days []string, tooBig bool) {
	if m.MaxBytes <= 0 || have+need <= m.MaxBytes {
		return nil, false
	}
	cached, _ := m.Cache.Days()
	free := m.MaxBytes - have
	for _, d := range cached {
		if keep[d.Day] {
			continue
		}
		if free >= need {
			break
		}
		days = append(days, d.Day)
		free += d.Bytes
	}
	return days, free < need
}

// Status is what a backfill is doing, or did last.
type Status struct {
	Running   bool      `json:"running"`
	Phase     string    `json:"phase,omitempty"` // "looking up", "downloading", "reading"
	Day       string    `json:"day,omitempty"`
	DaysDone  int       `json:"days_done"`
	DaysTotal int       `json:"days_total"`
	BytesDone int64     `json:"bytes_done"`
	BytesAll  int64     `json:"bytes_total"`
	Added     int       `json:"positions_added"`
	Error     string    `json:"error,omitempty"`
	Finished  time.Time `json:"finished,omitzero"`
	Lines     []string  `json:"lines,omitempty"` // what has happened, newest last
}

// Status returns a snapshot.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.st
	s.Lines = append([]string(nil), s.Lines...)
	return s
}

// ErrBusy says a backfill is already running.
var ErrBusy = errors.New("a backfill is already running")

// Start begins a backfill in the background for hexAddr alone, or all aircraft if it is empty.
func (m *Manager) Start(hexAddr string) error {
	m.mu.Lock()
	if m.st.Running {
		m.mu.Unlock()
		return ErrBusy
	}
	m.mu.Unlock()
	p, err := m.Plan(hexAddr)
	if err != nil {
		return err
	}
	if len(p.Days) == 0 {
		return errors.New("nothing to do: every day with data for these aircraft has been searched, or is not published yet")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.st.Running {
		m.mu.Unlock()
		cancel()
		return ErrBusy
	}
	m.st = Status{Running: true, DaysTotal: len(p.Days)}
	m.end = cancel
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		err := m.run(ctx, p)
		m.mu.Lock()
		m.st.Running, m.st.Phase, m.st.Finished = false, "", m.now()
		if err != nil {
			m.st.Error = err.Error()
		}
		m.mu.Unlock()
	}()
	return nil
}

// Cancel stops a running backfill after the file it is on; a half-downloaded part is kept for next time.
func (m *Manager) Cancel() {
	m.mu.Lock()
	if m.end != nil {
		m.end()
	}
	m.mu.Unlock()
}

// Wait returns when no backfill is running.
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) set(f func(s *Status)) {
	m.mu.Lock()
	f(&m.st)
	m.mu.Unlock()
}

// note adds a line to the status and the log.
func (m *Manager) note(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	m.log().Info("backfill: " + line)
	m.set(func(s *Status) {
		s.Lines = append(s.Lines, line)
		if len(s.Lines) > 40 {
			s.Lines = s.Lines[len(s.Lines)-40:]
		}
	})
}

func (m *Manager) run(ctx context.Context, p Plan) error {
	began := time.Now()
	m.log().Info("backfill: starting", "days", len(p.Days), "aircraft", p.Aircraft, "cache_bytes", p.CacheBytes, "cache_limit_bytes", m.MaxBytes)
	var added int
	pending := map[string]bool{}
	for _, d := range p.Days {
		pending[d.Day] = true
	}
	for i, d := range p.Days {
		if err := ctx.Err(); err != nil {
			m.note("stopped before %s", d.Day)
			return errors.New("stopped")
		}
		m.set(func(s *Status) { s.Day, s.DaysDone, s.BytesDone, s.BytesAll = d.Day, i, 0, 0 })
		if _, err := m.Cache.Parts(d.Day); err != nil {
			if err := m.fetchDay(ctx, d.Day, pending); err != nil {
				m.log().Error("backfill: stopped", "day", d.Day, "err", err)
				return err
			}
		}
		m.set(func(s *Status) { s.Phase = "reading" })
		res, err := ImportDay(m.Store, m.Cache, d.Day, p.hexes[d.Day], m.now())
		if err != nil {
			m.log().Error("backfill: reading the day failed", "day", d.Day, "err", err,
				"hint", "the cached files may be damaged or incomplete (delete them from the cache to fetch again), or the archive layout changed")
			return fmt.Errorf("%s: %w", d.Day, err)
		}
		delete(pending, d.Day)
		added += res.Total()
		m.set(func(s *Status) { s.Added = added })
		m.note("%s: %d positions added for %d of %d aircraft (%d trace files in the archive, read in %s)",
			d.Day, res.Total(), len(res.Positions), res.Searched, res.Traces, res.Elapsed.Round(time.Second))
		if res.Searched > 0 && res.Found == 0 {
			m.log().Warn("backfill: none of the aircraft had a trace on that day", "day", d.Day, "searched", res.Searched, "traces_in_archive", res.Traces,
				"hint", "plausible if they were parked, but if this repeats check that trace file names still use lower-case hex")
		}
		m.set(func(s *Status) { s.DaysDone = i + 1 })
		if i+1 < len(p.Days) {
			select {
			case <-ctx.Done():
			case <-time.After(dayPause):
			}
		}
	}
	m.log().Info("backfill: finished", "days", len(p.Days), "positions_added", added, "took", time.Since(began).Round(time.Second))
	return nil
}

// fetchDay makes room for a day and downloads it.
func (m *Manager) fetchDay(ctx context.Context, day string, pending map[string]bool) error {
	m.set(func(s *Status) { s.Phase = "looking up" })
	rel, err := m.Fetch.Release(ctx, day)
	if err != nil {
		if errors.Is(err, ErrNotPublished) {
			return fmt.Errorf("%s has not been published by adsb.lol (it usually is by 03:30Z the next day)", day)
		}
		return err
	}
	have := m.Cache.Bytes()
	evict, tooBig := m.evictions(have, rel.Bytes(), pending)
	if tooBig {
		m.log().Error("backfill: the cache is full of days still to be read", "limit_bytes", m.MaxBytes, "cache_bytes", have, "needed_bytes", rel.Bytes(),
			"hint", "raise backfill.cache_max_gib, or backfill fewer aircraft at a time")
		return fmt.Errorf("the cache limit (%s) leaves no room for %s (%s): raise backfill.cache_max_gib", gib(m.MaxBytes), day, gib(rel.Bytes()))
	}
	for _, old := range evict {
		freed, err := m.Cache.Remove(old)
		if err != nil {
			return fmt.Errorf("making room: %w", err)
		}
		m.note("deleted %s from the cache (%s) to stay within the limit", old, gib(freed))
	}
	m.set(func(s *Status) { s.Phase, s.BytesAll = "downloading", rel.Bytes() })
	m.note("downloading %s (%s)", day, gib(rel.Bytes()))
	if err := m.Fetch.Download(ctx, m.Cache, rel, func(done, total int64) {
		m.set(func(s *Status) { s.BytesDone, s.BytesAll = done, total })
	}); err != nil {
		return err
	}
	return nil
}

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }
