// Package adsb queries ADSBexchange-v2-style APIs (adsb.lol, adsb.fi,
// airplanes.live) with fallback across sources and a per-source rate limit.
package adsb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Source struct {
	Name    string
	BaseURL string // e.g. https://api.adsb.lol/v2
	RegPath string // "reg" (adsb.lol) or "registration" (adsb.fi)
}

var (
	ADSBLol = Source{Name: "adsb.lol", BaseURL: "https://api.adsb.lol/v2", RegPath: "reg"}
	ADSBFi  = Source{Name: "adsb.fi", BaseURL: "https://opendata.adsb.fi/api/v2", RegPath: "registration"}
)

// Aircraft is one entry of the "ac" array. Only fields we use are mapped.
type Aircraft struct {
	Hex      string   `json:"hex"`
	Flight   string   `json:"flight"` // callsign, space padded in the feed
	Reg      string   `json:"r"`
	Type     string   `json:"t"`
	Lat      *float64 `json:"lat"`
	Lon      *float64 `json:"lon"`
	AltBaro  AltBaro  `json:"alt_baro"`
	GS       *float64 `json:"gs"`
	Track    *float64 `json:"track"`
	BaroRate *float64 `json:"baro_rate"`
	Squawk   string   `json:"squawk"`
	SeenPos  *float64 `json:"seen_pos"` // seconds since the position was last updated
	Seen     float64  `json:"seen"`

	Source string `json:"-"` // which source returned it
}

// Callsign returns the trimmed, upper-cased callsign.
func (a Aircraft) Callsign() string { return strings.ToUpper(strings.TrimSpace(a.Flight)) }

// HasPosition reports whether lat/lon are present and fresher than maxAge.
func (a Aircraft) HasPosition(maxAge time.Duration) bool {
	if a.Lat == nil || a.Lon == nil {
		return false
	}
	return a.SeenPos == nil || *a.SeenPos <= maxAge.Seconds()
}

// AltBaro is a number of feet, or the string "ground".
type AltBaro struct {
	Feet     *float64
	OnGround bool
}

func (a *AltBaro) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		a.OnGround = s == "ground"
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	a.Feet = &f
	return nil
}

type response struct {
	AC  []Aircraft `json:"ac"`
	Msg string     `json:"msg"`
}

// Client tries each source in order and returns the first that answers
// without error. An empty result is a valid answer ("not in coverage").
type Client struct {
	Sources    []Source
	HTTP       *http.Client
	MinSpacing time.Duration // between requests to the same source; default 1.1s
	UserAgent  string

	mu   sync.Mutex
	next map[string]time.Time
}

func NewClient(sources ...Source) *Client {
	if len(sources) == 0 {
		sources = []Source{ADSBLol, ADSBFi}
	}
	return &Client{Sources: sources, HTTP: &http.Client{Timeout: 15 * time.Second},
		UserAgent: "FullFlight/0.1 (personal flight tracker)"}
}

func (c *Client) Hex(ctx context.Context, hex string) ([]Aircraft, error) {
	return c.query(ctx, func(s Source) string { return "/hex/" + url.PathEscape(strings.ToLower(hex)) })
}

func (c *Client) Callsign(ctx context.Context, cs string) ([]Aircraft, error) {
	return c.query(ctx, func(s Source) string { return "/callsign/" + url.PathEscape(strings.ToUpper(cs)) })
}

func (c *Client) Reg(ctx context.Context, reg string) ([]Aircraft, error) {
	return c.query(ctx, func(s Source) string { return "/" + s.RegPath + "/" + url.PathEscape(strings.ToUpper(reg)) })
}

func (c *Client) query(ctx context.Context, path func(Source) string) ([]Aircraft, error) {
	var errs []error
	for _, s := range c.Sources {
		ac, err := c.get(ctx, s, path(s))
		if err == nil {
			for i := range ac {
				ac[i].Source = s.Name
				ac[i].Hex = strings.ToUpper(ac[i].Hex)
			}
			return ac, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

func (c *Client) get(ctx context.Context, s Source, path string) ([]Aircraft, error) {
	if err := c.wait(ctx, s.Name); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("bad JSON: %w", err)
	}
	return r.AC, nil
}

// wait reserves the next request slot for a source, sleeping if needed.
func (c *Client) wait(ctx context.Context, name string) error {
	spacing := c.MinSpacing
	if spacing == 0 {
		spacing = 1100 * time.Millisecond
	}
	c.mu.Lock()
	if c.next == nil {
		c.next = map[string]time.Time{}
	}
	at := c.next[name]
	now := time.Now()
	if at.Before(now) {
		at = now
	}
	c.next[name] = at.Add(spacing)
	c.mu.Unlock()

	select {
	case <-time.After(time.Until(at)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
