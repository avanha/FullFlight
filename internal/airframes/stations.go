package airframes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Station is a receiving station as airframes.io lists it: a volunteer's receiver, or one of its own satellite receivers. The
// position is where airframes.io shows it, which for a station that asked for it is fuzzed.
type Station struct {
	Ident          string     `json:"ident"`
	Latitude       *float64   `json:"latitude"`
	Longitude      *float64   `json:"longitude"`
	Fuzzed         bool       `json:"useFuzzedLocation"`
	CountryCode    FlexString `json:"countryCode"`
	CountryName    FlexString `json:"countryName"`
	SourceType     FlexString `json:"sourceType"` // vdl, acars, hfdl, ...
	Application    FlexString `json:"sourceApplication"`
	Status         FlexString `json:"status"`
	Flagged        bool       `json:"flagged"`
	FlaggedReason  FlexString `json:"flaggedReason"`
	LastReportAt   time.Time  `json:"lastReportAt"`
	MessagesCount  int64      `json:"messagesCount"`
	NearestAirport FlexString `json:"nearestAirportIcao"`
}

// maxStationList bounds the list; it was about 8.5 MB for some eight thousand stations.
const maxStationList = 64 << 20

// Stations fetches the whole list of stations in one request. It is a large answer that changes slowly: ask for it rarely.
func (c *Client) Stations(ctx context.Context) ([]Station, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/stations", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	hc := *c.HTTP
	hc.Timeout = 2 * time.Minute // a few megabytes
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return nil, &RateLimitError{RetryAfter: time.Duration(retry) * time.Second}
	default:
		return nil, fmt.Errorf("airframes.io: stations: HTTP %d", resp.StatusCode)
	}
	var out []Station
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxStationList)).Decode(&out); err != nil {
		return nil, fmt.Errorf("airframes.io: stations: bad JSON (the list's layout may have changed): %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("airframes.io: stations: the list is empty")
	}
	return out, nil
}
