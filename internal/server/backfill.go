package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/avanha/fullflight/internal/history"
)

var hexAddr = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

type backfillJSON struct {
	Enabled    bool           `json:"enabled"`
	CacheDir   string         `json:"cache_dir,omitempty"`
	CacheBytes int64          `json:"cache_bytes"`
	CacheLimit int64          `json:"cache_limit_bytes"`
	Status     history.Status `json:"status"`
}

// backfillStatus tells the menu whether backfill is on, how full the cache is, and what a running one is doing.
func (s *Server) backfillStatus(w http.ResponseWriter, r *http.Request) {
	m := s.Backfill
	if m == nil {
		writeJSON(w, http.StatusOK, backfillJSON{})
		return
	}
	writeJSON(w, http.StatusOK, backfillJSON{Enabled: true, CacheDir: m.Cache.Dir, CacheBytes: m.Cache.Bytes(), CacheLimit: m.MaxBytes, Status: m.Status()})
}

func (s *Server) backfillPlan(w http.ResponseWriter, r *http.Request) {
	if s.Backfill == nil {
		fail(w, http.StatusConflict, "backfill is off: set backfill.enabled and backfill.cache_dir in the config")
		return
	}
	var only []string // days=2026-10-31,2026-10-30 selects those; absent selects all
	if v, ok := r.URL.Query()["days"]; ok {
		only = splitDays(v[0])
	}
	p, err := s.Backfill.Plan(strings.TrimSpace(r.URL.Query().Get("hex")), only)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Days == nil {
		p.Days = []history.PlanDay{}
	}
	writeJSON(w, http.StatusOK, p)
}

// splitDays reads a list of days; an empty one is an empty (not absent) selection.
func splitDays(v string) []string {
	out := []string{}
	for _, d := range strings.Split(v, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// backfillStart begins a backfill for one aircraft ({"hex": "7C4926"}) or, with no hex, every aircraft; "days" lists the days
// to read (the days from the plan), and leaving it out means all of them.
func (s *Server) backfillStart(w http.ResponseWriter, r *http.Request) {
	if s.Backfill == nil {
		fail(w, http.StatusConflict, "backfill is off: set backfill.enabled and backfill.cache_dir in the config")
		return
	}
	var in struct {
		Hex  string   `json:"hex"`
		Days []string `json:"days"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if in.Hex != "" && !hexAddr.MatchString(in.Hex) {
		fail(w, http.StatusBadRequest, "hex must be six hex digits")
		return
	}
	switch err := s.Backfill.Start(strings.TrimSpace(in.Hex), in.Days); {
	case errors.Is(err, history.ErrBusy):
		fail(w, http.StatusConflict, err.Error())
	case err != nil:
		fail(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, s.Backfill.Status())
	}
}

func (s *Server) backfillCancel(w http.ResponseWriter, r *http.Request) {
	if s.Backfill == nil {
		fail(w, http.StatusConflict, "backfill is off")
		return
	}
	s.Backfill.Cancel()
	writeJSON(w, http.StatusAccepted, s.Backfill.Status())
}
