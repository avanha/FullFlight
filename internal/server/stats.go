package server

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/version"
)

var started = time.Now()

type requestsJSON struct {
	Host      string `json:"host"`
	Today     int    `json:"today"`
	Yesterday int    `json:"yesterday"`
	Week      int    `json:"week"`  // the last seven UTC days, today included
	Month     int    `json:"month"` // this UTC calendar month
	Total     int    `json:"total"`
	Failed    int    `json:"failed"`  // of the total: no answer, or an HTTP error
	Limited   int    `json:"limited"` // of the total: HTTP 429
	Since     string `json:"since"`   // the first day counted
	Note      string `json:"note,omitempty"`
}

type statsHoldJSON struct {
	Name   string    `json:"name"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

type cacheJSON struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files,omitempty"`
	Limit int64  `json:"limit,omitempty"`
	Dir   string `json:"dir,omitempty"`
}

type statsJSON struct {
	Version  string          `json:"version"`
	Now      time.Time       `json:"now"`
	Started  time.Time       `json:"started"`
	Oldest   time.Time       `json:"oldest,omitzero"` // the oldest message or position
	DBBytes  int64           `json:"db_bytes"`
	Free     int64           `json:"db_free_bytes"`
	Tables   []tableJSON     `json:"tables"`
	Holdings store.Holdings  `json:"holdings"`
	Requests []requestsJSON  `json:"requests"`
	Holds    []statsHoldJSON `json:"holds"`
	Caches   []cacheJSON     `json:"caches"`
	Busiest  []busyJSON      `json:"busiest"`
}

type tableJSON struct {
	Name  string `json:"name"`
	Rows  int    `json:"rows"`
	Bytes int64  `json:"bytes"`
}

type busyJSON struct {
	Hex       string `json:"hex"`
	Tail      string `json:"tail,omitempty"`
	Messages  int    `json:"messages"`
	Positions int    `json:"positions"`
}

// hostNotes say what a host's requests cost, where that is known.
var hostNotes = map[string]string{
	"aerodatabox.p.rapidapi.com": "a flight lookup is probably 2 of the plan's units; the free plan has 400 a month",
}

// requestsBody sums the request tallies by host over the periods the page shows.
func requestsBody(counts []store.RequestCount, now time.Time) []requestsJSON {
	today := now.UTC().Format("2006-01-02")
	yesterday := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	week := now.UTC().AddDate(0, 0, -6).Format("2006-01-02")
	month := now.UTC().Format("2006-01")
	by := map[string]*requestsJSON{}
	for _, c := range counts {
		r := by[c.Host]
		if r == nil {
			r = &requestsJSON{Host: c.Host, Since: c.Day, Note: hostNotes[c.Host]}
			by[c.Host] = r
		}
		r.Total += c.Requests
		r.Failed += c.Failed
		r.Limited += c.Limited
		if c.Day == today {
			r.Today += c.Requests
		}
		if c.Day == yesterday {
			r.Yesterday += c.Requests
		}
		if c.Day >= week {
			r.Week += c.Requests
		}
		if strings.HasPrefix(c.Day, month) {
			r.Month += c.Requests
		}
	}
	out := make([]requestsJSON, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Week != out[j].Week {
			return out[i].Week > out[j].Week
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// dirSizes remembers what a directory held, so a page that is reloaded does not walk a cache of thousands of files each time.
var dirSizes = struct {
	sync.Mutex
	at   map[string]time.Time
	size map[string][2]int64
}{at: map[string]time.Time{}, size: map[string][2]int64{}}

const dirSizeFor = time.Minute

func dirSize(dir string, now time.Time) (bytes int64, files int) {
	dirSizes.Lock()
	defer dirSizes.Unlock()
	if at, ok := dirSizes.at[dir]; ok && now.Sub(at) < dirSizeFor {
		v := dirSizes.size[dir]
		return v[0], int(v[1])
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			bytes += fi.Size()
			files++
		}
		return nil
	})
	dirSizes.at[dir], dirSizes.size[dir] = now, [2]int64{bytes, int64(files)}
	return bytes, files
}

// usageFor is how long the size of the database is remembered: measuring it reads every page, which holds up the writers
// (half a second for 70 MB), and the page asks every 30 seconds.
const usageFor = 5 * time.Minute

func (s *Server) usage(now time.Time) (store.Usage, error) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if !s.usageAt.IsZero() && now.Sub(s.usageAt) < usageFor {
		return s.usageVal, nil
	}
	u, err := s.Store.Usage()
	if err == nil {
		s.usageAt, s.usageVal = now, u
	}
	return u, err
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	out := statsJSON{Version: version.String(), Now: now, Started: started}
	u, err := s.usage(now)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.DBBytes, out.Free, out.Oldest = u.DBBytes, u.FreeBytes, u.Oldest
	for _, t := range u.Tables {
		out.Tables = append(out.Tables, tableJSON{t.Name, t.Rows, t.Bytes})
	}
	for i, a := range u.Aircraft {
		if i == 10 {
			break
		}
		out.Busiest = append(out.Busiest, busyJSON{a.Hex, a.Tail, a.Messages, a.Positions})
	}
	if out.Holdings, err = s.Store.Holdings(now); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.Store.RequestCounts(time.Time{})
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Requests = requestsBody(counts, now)
	out.Holds = []statsHoldJSON{}
	if holds, err := s.Store.ProviderHolds(now); err == nil {
		for name, h := range holds {
			out.Holds = append(out.Holds, statsHoldJSON{name, h.Until, h.Reason})
		}
		sort.Slice(out.Holds, func(i, j int) bool { return out.Holds[i].Name < out.Holds[j].Name })
	}
	out.Caches = []cacheJSON{}
	if s.Tiles != nil {
		b, n := dirSize(s.Tiles.Dir, now)
		out.Caches = append(out.Caches, cacheJSON{Name: "OpenStreetMap tiles", Bytes: b, Files: n, Limit: s.Tiles.MaxBytes, Dir: s.Tiles.Dir})
	}
	if s.WeatherDir != "" {
		b, n := dirSize(s.WeatherDir, now)
		out.Caches = append(out.Caches, cacheJSON{Name: "recorded radar", Bytes: b, Files: n, Dir: s.WeatherDir})
	}
	writeJSON(w, http.StatusOK, out)
}
