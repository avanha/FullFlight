// Package server serves the JSON API and the static UI.
package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airports"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

//go:embed static
var static embed.FS

type Server struct {
	Store    *store.Store
	Username string
	Password string // empty disables auth (config only allows that on loopback)
	Now      func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/watches", s.listWatches)
	mux.HandleFunc("POST /api/watches", s.addWatch)
	mux.HandleFunc("DELETE /api/watches/{id}", s.deleteWatch)
	mux.HandleFunc("GET /api/track/{hex}", s.track)
	mux.HandleFunc("GET /api/airframes", s.airframes)
	return s.auth(mux)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.Password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(s.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="FullFlight"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

type watchJSON struct {
	ID       int64     `json:"id"`
	Kind     string    `json:"kind"`
	Ident    string    `json:"ident"`
	Date     string    `json:"date,omitempty"`
	Dest     string    `json:"dest,omitempty"` // ICAO code
	DestName string    `json:"dest_name,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Status   string    `json:"status"`
	Hex      string    `json:"hex,omitempty"`
	Tail     string    `json:"tail,omitempty"`
	Callsign string    `json:"callsign,omitempty"`
}

func toJSON(e watchlist.Entry) watchJSON {
	w := watchJSON{ID: e.ID, Kind: string(e.Kind), Ident: e.Ident, Date: e.Date, Dest: e.Dest, Start: e.Start, End: e.End,
		Status: string(e.Status), Hex: e.Hex, Tail: e.Tail, Callsign: e.Callsign}
	if a, ok := airports.Lookup(e.Dest); ok {
		w.DestName = a.Name
	}
	return w
}

func (s *Server) listWatches(w http.ResponseWriter, r *http.Request) {
	es, err := s.Store.ListWatches(false)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]watchJSON, 0, len(es))
	for _, e := range es {
		out = append(out, toJSON(e))
	}
	writeJSON(w, 200, out)
}

// addWatch takes {"input": "UA869 2026-10-31 SFO"} or {"input": "B-LRM", "until": "<RFC3339>"}.
func (s *Server) addWatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input string `json:"input"`
		Until string `json:"until"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		fail(w, 400, "bad JSON")
		return
	}
	var until time.Time
	if in.Until != "" {
		var err error
		if until, err = time.Parse(time.RFC3339, in.Until); err != nil {
			fail(w, 400, "until must be RFC 3339, e.g. 2026-11-02T00:00:00Z")
			return
		}
	}
	e, err := watchlist.Parse(in.Input, s.now(), until)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if e.Dest != "" {
		a, ok := airports.Lookup(e.Dest)
		if !ok {
			fail(w, 400, "unknown airport "+strconv.Quote(e.Dest))
			return
		}
		e.Dest = a.ICAO
	}
	e, err = s.Store.AddWatch(e)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, toJSON(e))
}

func (s *Server) deleteWatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	if err := s.Store.DeleteWatch(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pointJSON struct {
	T        time.Time `json:"t"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	AltFt    *float64  `json:"alt_ft"`
	GS       *float64  `json:"gs"`
	Track    *float64  `json:"track"`
	Callsign string    `json:"callsign"`
	Source   string    `json:"source"`
}

// track returns the positions of one aircraft, default the last 48 hours.
func (s *Server) track(w http.ResponseWriter, r *http.Request) {
	hex := strings.ToUpper(r.PathValue("hex"))
	hours := 48
	if h, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && h > 0 && h <= 24*14 {
		hours = h
	}
	now := s.now()
	ps, err := s.Store.Track(hex, now.Add(-time.Duration(hours)*time.Hour), now.Add(time.Minute))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]pointJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, pointJSON{p.T, p.Lat, p.Lon, p.AltFt, p.GS, p.Track, p.Callsign, p.Source})
	}
	writeJSON(w, 200, out)
}

func (s *Server) airframes(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.ListAirframes()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	type row struct {
		Hex       string    `json:"hex"`
		Tail      string    `json:"tail"`
		Type      string    `json:"type,omitempty"`
		Source    string    `json:"source"`
		FirstSeen time.Time `json:"first_seen"`
		LastSeen  time.Time `json:"last_seen"`
	}
	out := make([]row, 0, len(as))
	for _, a := range as {
		out = append(out, row{a.Hex, a.Tail, a.Type, a.Source, a.FirstSeen, a.LastSeen})
	}
	writeJSON(w, 200, out)
}
