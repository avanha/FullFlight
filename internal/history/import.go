package history

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// Result says what importing one day did.
type Result struct {
	Day       string
	Searched  int            // aircraft looked for
	Skipped   int            // aircraft already searched for in this day
	Found     int            // aircraft that had a trace
	Positions map[string]int // positions added, by upper-case hex, for those that gave any
	Members   int            // files in the archive
	Traces    int            // of them, aircraft trace files
	Elapsed   time.Duration
}

// Total is the number of positions added.
func (r Result) Total() (n int) {
	for _, c := range r.Positions {
		n += c
	}
	return n
}

// ImportDay reads the cached archive of day (YYYY-MM-DD) once, and stores the positions of every aircraft in hexes (upper
// case) that has not been searched for in that day before. Each is then marked as searched, including those with no trace,
// so the day is not read for them again. Nothing is marked unless the whole archive was read.
func ImportDay(st *store.Store, c Cache, day string, hexes []string, now time.Time) (Result, error) {
	res := Result{Day: day, Positions: map[string]int{}}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return res, fmt.Errorf("day %q: want YYYY-MM-DD", day)
	}
	done, err := st.BackfillScanned(day)
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	var todo []string
	for _, h := range hexes {
		h = strings.ToUpper(h)
		if done[h] {
			res.Skipped++
			continue
		}
		want[strings.ToLower(h)] = true
		todo = append(todo, h)
	}
	res.Searched = len(todo)
	if len(todo) == 0 {
		return res, nil
	}
	parts, err := c.Parts(day)
	if err != nil {
		return res, err
	}
	added := map[string]int{}
	found := map[string]bool{}
	began := time.Now()
	stats, err := Scan(parts, want, func(hex string, r io.Reader) error {
		ps, err := ParseTrace(r)
		if err != nil {
			return fmt.Errorf("%w (readsb's trace format may have changed: expected gzipped JSON with a \"trace\" list of [offset, lat, lon, alt, ...] rows)", err)
		}
		h := strings.ToUpper(hex)
		n, err := st.AddHistory(h, ps)
		if err != nil {
			return err
		}
		found[h] = true
		added[h] += n
		return nil
	})
	res.Members, res.Traces, res.Elapsed = stats.Members, stats.Traces, time.Since(began)
	if err != nil {
		return res, err
	}
	for _, h := range todo {
		if err := st.MarkBackfill(day, h, added[h], now); err != nil {
			return res, err
		}
		if found[h] {
			res.Found++
		}
		if added[h] > 0 {
			res.Positions[h] = added[h]
		}
	}
	return res, nil
}
