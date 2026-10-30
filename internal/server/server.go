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

	"github.com/avanha/fullflight/internal/airlines"
	"github.com/avanha/fullflight/internal/airports"
	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
	"github.com/avanha/fullflight/internal/waypoints"
)

//go:embed static
var static embed.FS

type Server struct {
	Store    *store.Store
	Nav      *navdata.DB // optional waypoint database
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
	mux.HandleFunc("GET /api/messages/{hex}", s.messages)
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
	ID       int64       `json:"id"`
	Kind     string      `json:"kind"`
	Ident    string      `json:"ident"`
	Date     string      `json:"date,omitempty"`
	Dest     string      `json:"dest,omitempty"` // ICAO code
	DestName string      `json:"dest_name,omitempty"`
	Start    time.Time   `json:"start"`
	End      time.Time   `json:"end"`
	Status   string      `json:"status"`
	Hex      string      `json:"hex,omitempty"`
	Tail     string      `json:"tail,omitempty"`
	Callsign string      `json:"callsign,omitempty"`
	Type     string      `json:"type,omitempty"` // aircraft type, e.g. A359
	LastPos  *lastPos    `json:"last_pos,omitempty"`
	Route    *routeJSON  `json:"route,omitempty"`
	Flight   *flightJSON `json:"flight,omitempty"` // tail and hex watches only: the flight it is operating
}

// flightJSON is the flight an aircraft is currently operating, from its newest ADS-B callsign
// or the flight number in its newest message, whichever is later.
type flightJSON struct {
	Callsign string    `json:"callsign,omitempty"` // ICAO form, e.g. CPA113
	Number   string    `json:"number,omitempty"`   // IATA form, e.g. CX113
	T        time.Time `json:"t"`
	Source   string    `json:"source"` // "ADS-B" or "messages"
	Stale    bool      `json:"stale,omitempty"`
}

func (s *Server) flight(hex string) *flightJSON {
	var id, src string
	var at time.Time
	if cs, t, ok := s.Store.LastCallsign(hex); ok {
		id, at, src = cs, t, "ADS-B"
	}
	if f, ok := s.Store.LastFlight(hex); ok && f.T.After(at) {
		id, at, src = firstNonEmpty(f.Callsign, f.Flight), f.T, "messages"
	}
	if id == "" {
		return nil
	}
	age := s.now().Sub(at)
	if age > InfoHideAfter {
		return nil
	}
	out := &flightJSON{T: at, Source: src, Stale: age > InfoFreshFor}
	out.Callsign, out.Number = airlines.Forms(id)
	if out.Callsign == "" && out.Number == "" {
		out.Callsign = strings.ToUpper(id) // an airline we have no table entry for: show it as received
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// How long something the aircraft says about itself (its destination, its flight) stays
// believable. Reports go out every ~20 minutes in flight, so one older than InfoFreshFor means
// we have lost contact, or the aircraft has landed and moved on to another sector. After
// InfoHideAfter we stop showing it at all.
const (
	InfoFreshFor  = 90 * time.Minute
	InfoHideAfter = 24 * time.Hour
)

// routeJSON is the origin and destination the aircraft last reported (REP081).
type routeJSON struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	FromName string    `json:"from_name,omitempty"`
	ToName   string    `json:"to_name,omitempty"`
	T        time.Time `json:"t"`
	Stale    bool      `json:"stale,omitempty"` // older than InfoFreshFor: may be out of date
	Source   string    `json:"source"`
}

func (s *Server) route(hex string) *routeJSON {
	r, ok := s.Store.LastRoute(hex)
	if !ok {
		return nil
	}
	age := s.now().Sub(r.T)
	if age > InfoHideAfter {
		return nil
	}
	out := &routeJSON{From: r.From, To: r.To, T: r.T, Stale: age > InfoFreshFor, Source: "REP081"}
	if a, ok := airports.Lookup(r.From); ok {
		out.FromName = a.Name
	}
	if a, ok := airports.Lookup(r.To); ok {
		out.ToName = a.Name
	}
	return out
}

// lastPos is the newest position we hold for a watched aircraft, from ADS-B or
// from a datalink message, whichever is later.
type lastPos struct {
	T     time.Time `json:"t"`
	Kind  string    `json:"kind"` // ADS-B, ADS-C, CPDLC or REP081
	Lat   float64   `json:"lat"`
	Lon   float64   `json:"lon"`
	AltFt *float64  `json:"alt_ft,omitempty"`
}

func (s *Server) lastPosition(hex string) *lastPos {
	var best *lastPos
	if p, ok := s.Store.LastPosition(hex); ok {
		best = &lastPos{T: p.T, Kind: "ADS-B", Lat: p.Lat, Lon: p.Lon, AltFt: p.AltFt}
	}
	if m, ok := s.Store.LastMessagePosition(hex); ok && (best == nil || m.T.After(best.T)) {
		kind := map[string]string{"adsc": "ADS-C", "cpdlc": "CPDLC", "miam": "REP081", "fms_position": "FMS"}[m.Kind]
		if kind == "" {
			kind = m.Kind
		}
		best = &lastPos{T: m.T, Kind: kind, Lat: m.Lat, Lon: m.Lon, AltFt: m.AltFt}
	}
	return best
}

func toJSON(e watchlist.Entry) watchJSON {
	w := watchJSON{ID: e.ID, Kind: string(e.Kind), Ident: e.Ident, Date: e.Date, Dest: e.Dest, Start: e.Start, End: e.End,
		Status: string(e.Status), Hex: e.Hex, Tail: e.Tail, Callsign: e.Callsign}
	if a, ok := airports.Lookup(e.Dest); ok {
		w.DestName = a.Name
	}
	return w
}

// withAircraft adds what we know about the followed aircraft.
func (s *Server) withAircraft(j watchJSON) watchJSON {
	if j.Hex != "" {
		j.Type = s.Store.TypeForHex(j.Hex)
		j.LastPos = s.lastPosition(j.Hex)
		j.Route = s.route(j.Hex)
		if j.Kind != string(watchlist.KindFlight) { // a flight watch already is a flight
			j.Flight = s.flight(j.Hex)
		}
	}
	return j
}

func (s *Server) listWatches(w http.ResponseWriter, r *http.Request) {
	es, err := s.Store.ListWatches(false)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]watchJSON, 0, len(es))
	for _, e := range es {
		out = append(out, s.withAircraft(toJSON(e)))
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
	writeJSON(w, 201, s.withAircraft(toJSON(e)))
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
	VRate    *float64  `json:"vrate"` // ft/min, positive climbing
	OnGround bool      `json:"on_ground,omitempty"`
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
		out = append(out, pointJSON{T: p.T, Lat: p.Lat, Lon: p.Lon, AltFt: p.AltFt, GS: p.GS, Track: p.Track, VRate: p.VRate, OnGround: p.OnGround, Callsign: p.Callsign, Source: p.Source})
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

type messageJSON struct {
	ID             int64      `json:"id"`
	T              time.Time  `json:"t"`
	Label          string     `json:"label"`
	Kind           string     `json:"kind"`
	Direction      string     `json:"dir,omitempty"`
	Summary        string     `json:"summary"`
	Undecoded      bool       `json:"undecoded,omitempty"` // we have no decoder for it (vendor data, MIAM fragments)
	Detail         string     `json:"detail,omitempty"`
	Raw            string     `json:"raw"`
	Tail           string     `json:"tail,omitempty"`
	Flight         string     `json:"flight,omitempty"`
	Callsign       string     `json:"callsign,omitempty"`
	Station        string     `json:"station,omitempty"`
	RxCount        int        `json:"rx_count"`
	Lat            *float64   `json:"lat,omitempty"`
	Lon            *float64   `json:"lon,omitempty"`
	AltFt          *float64   `json:"alt_ft,omitempty"`
	PosT           *time.Time `json:"pos_t,omitempty"`
	Suspect        string     `json:"suspect,omitempty"`
	TimeNote       string     `json:"time_note,omitempty"`
	TimeUnverified bool       `json:"time_unverified,omitempty"`
	Mismatch       string     `json:"mismatch,omitempty"`
	AFTail         string     `json:"af_tail,omitempty"`
	AFFlight       string     `json:"af_flight,omitempty"`
	Route          any        `json:"route,omitempty"`

	Wx        *wxJSON        `json:"wx,omitempty"`
	GS        *float64       `json:"gs,omitempty"`  // knots, as the report gave it
	Trk       *float64       `json:"trk,omitempty"` // true track, degrees
	Waypoints []waypointJSON `json:"waypoints,omitempty"`
}

// wxJSON is the weather at a report: wind direction (the way it blows FROM, true), speed and temperature.
type wxJSON struct {
	DirDeg *float64 `json:"dir,omitempty"`
	Kt     *float64 `json:"kt,omitempty"`
	TempC  *float64 `json:"temp_c,omitempty"`
}

// waypointJSON is a waypoint a CPDLC position report named, with the best position we can give it.
type waypointJSON struct {
	Name   string     `json:"name"`
	Role   string     `json:"role"` // passed, next or then
	Lat    float64    `json:"lat"`
	Lon    float64    `json:"lon"`
	Exact  bool       `json:"exact"`
	Source string     `json:"source"` // ADS-C, database, interpolated or projected
	How    string     `json:"how"`
	T      *time.Time `json:"t,omitempty"`
	AltFt  *float64   `json:"alt_ft,omitempty"`
}

// messages returns an aircraft's decoded datalink messages, oldest first.
// Query: hours (default 48, max 336), limit (default 300, max 1000).
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	hex := strings.ToUpper(r.PathValue("hex"))
	q := r.URL.Query()
	hours, limit := 48, 300
	if h, err := strconv.Atoi(q.Get("hours")); err == nil && h > 0 && h <= 24*14 {
		hours = h
	}
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= 1000 {
		limit = l
	}
	now := s.now()
	ms, err := s.Store.Messages(hex, now.Add(-time.Duration(hours)*time.Hour), now.Add(time.Minute), limit)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	res := &waypoints.Resolver{Store: s.Store, Nav: s.Nav}
	out := make([]messageJSON, 0, len(ms))
	for _, m := range ms {
		j := messageJSON{Undecoded: m.Kind == "unknown" || m.Summary == "MIAM (not decoded)", ID: m.ID, T: m.T, Label: m.Label, Kind: m.Kind, Direction: m.Direction, Summary: m.Summary,
			Detail: m.Detail, Raw: m.Text, Tail: m.Tail, Flight: m.Flight, Callsign: m.Callsign, Station: m.Station,
			RxCount: m.RxCount, Lat: m.Lat, Lon: m.Lon, AltFt: m.AltFt, PosT: m.PosT, Suspect: m.Suspect, TimeNote: m.TimeNote, TimeUnverified: m.TimeFlag,
			Mismatch: m.Mismatch, AFTail: m.AFTail, AFFlight: m.AFFlight}
		if m.Route != "" {
			json.Unmarshal([]byte(m.Route), &j.Route)
		}
		if m.WxDir != nil || m.WxKt != nil || m.WxTemp != nil {
			j.Wx = &wxJSON{DirDeg: m.WxDir, Kt: m.WxKt, TempC: m.WxTemp}
		}
		j.GS, j.Trk = m.GS, m.Trk
		for _, w := range res.Resolve(m) {
			wj := waypointJSON{Name: w.Name, Role: w.Role, Lat: w.Lat, Lon: w.Lon, Exact: w.Exact, Source: w.Source, How: w.How, AltFt: w.AltFt}
			if !w.T.IsZero() {
				t := w.T
				wj.T = &t
			}
			j.Waypoints = append(j.Waypoints, wj)
		}
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}
