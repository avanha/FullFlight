package airframes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PageSize is the most messages one request returns (the API ignores larger limits).
const PageSize = 100

// FlexString decodes a JSON string, number or null into a string.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null":
		*f = ""
	case strings.HasPrefix(s, `"`):
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = FlexString(v)
	default:
		*f = FlexString(s)
	}
	return nil
}

// Message is the part of an airframes.io message we keep. Its derived fields
// (Tail, FlightNumber, Flight, the airports) are hints only: we decode the
// raw Text ourselves and treat disagreements as warnings.
type Message struct {
	ID            int64      `json:"id"`
	Timestamp     time.Time  `json:"timestamp"`
	Label         FlexString `json:"label"`
	BlockID       FlexString `json:"blockId"`
	MessageNumber FlexString `json:"messageNumber"`
	LinkDirection FlexString `json:"linkDirection"` // "uplink" / "downlink" / ""
	FromHex       FlexString `json:"fromHex"`
	ToHex         FlexString `json:"toHex"`
	Tail          FlexString `json:"tail"`
	FlightNumber  FlexString `json:"flightNumber"`
	Source        FlexString `json:"source"`
	SourceType    FlexString `json:"sourceType"`
	Frequency     *float64   `json:"frequency"`
	Text          FlexString `json:"text"`
	Departing     FlexString `json:"departingAirport"`
	Destination   FlexString `json:"destinationAirport"`
	Station       struct {
		Ident FlexString `json:"ident"`
	} `json:"station"`
	Flight struct {
		Flight FlexString `json:"flight"`
	} `json:"flight"`
	Airframe struct {
		Tail FlexString `json:"tail"`
		ICAO FlexString `json:"icao"`
	} `json:"airframe"`
}

// RateLimitError is returned on HTTP 429.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("airframes.io rate limit; retry after %v", e.RetryAfter)
}

// Messages returns up to PageSize messages to or from a hex address received
// in [since, until], newest first. Both bounds are required by the API.
func (c *Client) Messages(ctx context.Context, hex string, since, until time.Time) ([]Message, error) {
	q := url.Values{}
	q.Set("icao", strings.ToUpper(hex))
	q.Set("limit", strconv.Itoa(PageSize))
	q.Set("since", since.UTC().Format(time.RFC3339Nano))
	q.Set("until", until.UTC().Format(time.RFC3339Nano))
	return c.messages(ctx, q)
}

// SearchText returns the newest PageSize messages, across all aircraft, whose text contains s. It is how
// a flight number is traced to an aircraft when nothing else can: the flight number is in the text of
// many messages (flight plans, logons, performance reports), which name the airframe. Matches may be
// from earlier flights of the same number, or stray (a short string can occur inside compressed data).
func (c *Client) SearchText(ctx context.Context, s string) ([]Message, error) {
	q := url.Values{}
	q.Set("text", s)
	q.Set("limit", strconv.Itoa(PageSize))
	return c.messages(ctx, q)
}

func (c *Client) messages(ctx context.Context, q url.Values) ([]Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		wait := time.Minute
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			wait = time.Duration(s) * time.Second
		}
		return nil, &RateLimitError{RetryAfter: wait}
	default:
		return nil, fmt.Errorf("airframes.io: HTTP %d", resp.StatusCode)
	}
	var out []Message
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("airframes.io: bad JSON: %w", err)
	}
	// Stay under the limit: if the budget is nearly spent, wait out the window.
	if rem, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil && rem <= 2 {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if d := time.Until(time.Unix(reset, 0)); d > 0 && d < 2*time.Minute {
				select {
				case <-time.After(d):
				case <-ctx.Done():
				}
			}
		}
	}
	return out, nil
}
