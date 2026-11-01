package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/store"
)

// StationSource lists the receiving stations (airframes.io).
type StationSource interface {
	Stations(ctx context.Context) ([]airframes.Station, error)
}

// StationsRefreshEvery is the least time between fetches of the list of stations: it is a few megabytes, and changes slowly.
const StationsRefreshEvery = 6 * time.Hour

// MinTimedForVerdict is how many messages with a time of the aircraft's own a station needs before we say how late it is.
const MinTimedForVerdict = 5

type stationJSON struct {
	Ident      string         `json:"ident"`
	Messages   int            `json:"messages"` // ours
	Aircraft   int            `json:"aircraft"`
	First      time.Time      `json:"first"`
	Last       time.Time      `json:"last"`
	Transports map[string]int `json:"transports"`

	Timed     int     `json:"timed"` // messages carrying the aircraft's own time
	Late      int     `json:"late"`  // of them, more than an hour after it
	LateFrac  float64 `json:"late_frac"`
	MedianS   int64   `json:"median_s"`
	P90S      int64   `json:"p90_s"`
	ShortestS int64   `json:"shortest_s"`
	Verdict   string  `json:"verdict"` // "late", "sometimes", "prompt" or "unknown" (too few timed messages)

	// what airframes.io says, when its list has been fetched and has the station
	Known          bool      `json:"known"`
	Lat            *float64  `json:"lat,omitempty"`
	Lon            *float64  `json:"lon,omitempty"`
	Fuzzed         bool      `json:"fuzzed,omitempty"`
	Country        string    `json:"country,omitempty"`
	CountryCode    string    `json:"country_code,omitempty"`
	Application    string    `json:"application,omitempty"`
	SourceType     string    `json:"source_type,omitempty"`
	Flagged        bool      `json:"flagged,omitempty"`
	FlaggedReason  string    `json:"flagged_reason,omitempty"`
	AFMessages     int64     `json:"af_messages,omitempty"`
	AFLastReport   time.Time `json:"af_last_report,omitzero"`
	NearestAirport string    `json:"nearest_airport,omitempty"`
}

type stationsJSON struct {
	Fetched  *time.Time    `json:"fetched,omitempty"` // when the list of stations was last fetched
	CanFetch bool          `json:"can_fetch"`         // a source is configured
	NextAt   *time.Time    `json:"next_at,omitempty"` // the earliest it may be fetched again
	Stations []stationJSON `json:"stations"`
}

func verdictOf(l store.Lateness) string {
	switch {
	case l.Timed < MinTimedForVerdict:
		return "unknown"
	case float64(l.Late)/float64(l.Timed) >= 0.5:
		return "late"
	case l.Late > 0:
		return "sometimes"
	}
	return "prompt"
}

func (s *Server) stationsBody() (stationsJSON, error) {
	sts, err := s.Store.Stations()
	if err != nil {
		return stationsJSON{}, err
	}
	out := stationsJSON{CanFetch: s.StationList != nil, Stations: make([]stationJSON, 0, len(sts))}
	if f := s.Store.StationsFetched(); !f.IsZero() {
		out.Fetched = &f
		next := f.Add(StationsRefreshEvery)
		out.NextAt = &next
	}
	for _, st := range sts {
		j := stationJSON{Ident: st.Ident, Messages: st.Messages, Aircraft: st.Aircraft, First: st.First, Last: st.Last, Transports: st.Transports,
			Timed: st.Lateness.Timed, Late: st.Lateness.Late, Verdict: verdictOf(st.Lateness)}
		if st.Lateness.Timed > 0 {
			j.LateFrac = float64(st.Lateness.Late) / float64(st.Lateness.Timed)
			j.MedianS, j.P90S, j.ShortestS = int64(st.Lateness.Median.Seconds()), int64(st.Lateness.P90.Seconds()), int64(st.Lateness.Shortest.Seconds())
		}
		if i := st.Info; i != nil {
			j.Known, j.Lat, j.Lon, j.Fuzzed = true, i.Lat, i.Lon, i.Fuzzed
			j.Country, j.CountryCode, j.Application, j.SourceType = i.CountryName, i.CountryCode, i.Application, i.SourceType
			j.Flagged, j.FlaggedReason, j.AFMessages, j.AFLastReport, j.NearestAirport = i.Flagged, i.FlaggedReason, i.MessagesCount, i.LastReportAt, i.NearestAirport
		}
		out.Stations = append(out.Stations, j)
	}
	sort.SliceStable(out.Stations, func(i, j int) bool { return out.Stations[i].Messages > out.Stations[j].Messages })
	return out, nil
}

// listStations answers with every station our messages came through: what we know of each, and where it is.
func (s *Server) listStations(w http.ResponseWriter, r *http.Request) {
	body, err := s.stationsBody()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// refreshStations fetches airframes.io's list of stations (one request, a few megabytes) and keeps the ones we have messages from,
// at most every StationsRefreshEvery.
func (s *Server) refreshStations(w http.ResponseWriter, r *http.Request) {
	if s.StationList == nil {
		fail(w, http.StatusConflict, "no source of stations is configured")
		return
	}
	if f := s.Store.StationsFetched(); !f.IsZero() && s.now().Sub(f) < StationsRefreshEvery {
		fail(w, http.StatusTooManyRequests, fmt.Sprintf("the list was fetched %s ago; it changes slowly, so it is fetched at most every %s",
			s.now().Sub(f).Round(time.Minute), StationsRefreshEvery))
		return
	}
	list, err := s.StationList.Stations(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ours, err := s.Store.Stations()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	heard := map[string]bool{}
	for _, st := range ours {
		heard[st.Ident] = true
	}
	var keep []store.StationInfo
	for _, a := range list {
		if !heard[a.Ident] {
			continue
		}
		keep = append(keep, store.StationInfo{Ident: a.Ident, Lat: a.Latitude, Lon: a.Longitude, Fuzzed: a.Fuzzed, CountryCode: string(a.CountryCode),
			CountryName: string(a.CountryName), SourceType: string(a.SourceType), Application: string(a.Application), Status: string(a.Status),
			Flagged: a.Flagged, FlaggedReason: string(a.FlaggedReason), LastReportAt: a.LastReportAt, MessagesCount: a.MessagesCount,
			NearestAirport: string(a.NearestAirport)})
	}
	if len(keep) == 0 {
		fail(w, http.StatusBadGateway, fmt.Sprintf("none of our %d stations is in airframes.io's list of %d: has its layout changed?", len(heard), len(list)))
		return
	}
	if err := s.Store.SaveStations(keep, s.now()); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.listStations(w, r)
}
