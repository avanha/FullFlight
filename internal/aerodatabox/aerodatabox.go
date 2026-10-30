// Package aerodatabox looks a flight up by number at AeroDataBox (via RapidAPI): the aircraft
// flying it, its route, and where the provider last placed it. This works where ADS-B does not,
// such as mid-ocean, but it costs a request per call, so callers should ask sparingly.
package aerodatabox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const host = "aerodatabox.p.rapidapi.com"

// Source names positions that came from here.
const Source = "aerodatabox"

type Client struct {
	BaseURL string // default https://aerodatabox.p.rapidapi.com
	Key     string
	HTTP    *http.Client
}

func NewClient(key string) *Client {
	return &Client{BaseURL: "https://" + host, Key: key, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Flight is what we use of an AeroDataBox flight.
type Flight struct {
	Number   string // "CK 624"
	Callsign string // "CKK624"
	Status   string // "EnRoute", "Departed", "Arrived", "Expected", ...
	Hex      string
	Reg      string
	Model    string // "Boeing 777", not an ICAO type code
	From, To string // ICAO airport codes
	Departed time.Time
	Arrives  time.Time // best estimate

	// Where the provider last placed it, if it did.
	HasPos   bool
	Lat, Lon float64
	PosAt    time.Time
	GS       *float64 // knots
	Track    *float64
}

type wireTime struct {
	UTC string `json:"utc"`
}

type wireAirport struct {
	Airport struct {
		ICAO string `json:"icao"`
	} `json:"airport"`
	ScheduledTime wireTime `json:"scheduledTime"`
	RevisedTime   wireTime `json:"revisedTime"`
	PredictedTime wireTime `json:"predictedTime"`
	RunwayTime    wireTime `json:"runwayTime"`
}

type wireFlight struct {
	Departure wireAirport `json:"departure"`
	Arrival   wireAirport `json:"arrival"`
	Number    string      `json:"number"`
	CallSign  string      `json:"callSign"`
	Status    string      `json:"status"`
	Aircraft  struct {
		Reg   string `json:"reg"`
		ModeS string `json:"modeS"`
		Model string `json:"model"`
	} `json:"aircraft"`
	Location *struct {
		GroundSpeed struct {
			Kt float64 `json:"kt"`
		} `json:"groundSpeed"`
		TrueTrack struct {
			Deg float64 `json:"deg"`
		} `json:"trueTrack"`
		ReportedAtUTC string  `json:"reportedAtUtc"`
		Lat           float64 `json:"lat"`
		Lon           float64 `json:"lon"`
	} `json:"location"`
}

// ByNumber returns the flights with this IATA flight number (CK624) that departed on date (UTC,
// 2006-01-02), most relevant first. It returns none if the provider does not know the flight.
func (c *Client) ByNumber(ctx context.Context, number, date string) ([]Flight, error) {
	u := c.BaseURL + "/flights/number/" + url.PathEscape(number) + "/" + url.PathEscape(date) + "?withLocation=true&withAircraftImage=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-RapidAPI-Key", c.Key)
	req.Header.Set("X-RapidAPI-Host", host)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusNotFound:
		return nil, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("aerodatabox: HTTP %d: check aerodatabox.api_key", resp.StatusCode)
	default:
		return nil, fmt.Errorf("aerodatabox: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parse(b)
}

func parse(b []byte) ([]Flight, error) {
	var ws []wireFlight
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil, fmt.Errorf("aerodatabox: %w", err)
	}
	out := make([]Flight, 0, len(ws))
	for _, w := range ws {
		f := Flight{
			Number: w.Number, Callsign: strings.ToUpper(w.CallSign), Status: w.Status,
			Hex: strings.ToUpper(w.Aircraft.ModeS), Reg: strings.ToUpper(w.Aircraft.Reg), Model: w.Aircraft.Model,
			From: strings.ToUpper(w.Departure.Airport.ICAO), To: strings.ToUpper(w.Arrival.Airport.ICAO),
			Departed: first(w.Departure.RunwayTime, w.Departure.RevisedTime, w.Departure.ScheduledTime),
			Arrives:  first(w.Arrival.PredictedTime, w.Arrival.RevisedTime, w.Arrival.ScheduledTime),
		}
		if l := w.Location; l != nil && (l.Lat != 0 || l.Lon != 0) {
			if t, err := parseTime(l.ReportedAtUTC); err == nil {
				f.HasPos, f.Lat, f.Lon, f.PosAt = true, l.Lat, l.Lon, t
				gs, trk := l.GroundSpeed.Kt, l.TrueTrack.Deg
				if gs > 0 {
					f.GS, f.Track = &gs, &trk
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// Airborne reports whether the provider says the flight is in the air.
func (f Flight) Airborne() bool {
	return f.Status == "EnRoute" || f.Status == "Departed" || f.Status == "Approaching"
}

func first(ts ...wireTime) time.Time {
	for _, t := range ts {
		if v, err := parseTime(t.UTC); err == nil {
			return v
		}
	}
	return time.Time{}
}

// parseTime reads "2026-10-30 17:11Z" and "2026-10-30 21:09".
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "Z")
	return time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
}
