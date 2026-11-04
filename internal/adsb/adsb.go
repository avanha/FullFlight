// Package adsb queries ADSBexchange-v2-style APIs (adsb.lol, adsb.fi,
// airplanes.live) with fallback across sources and a per-source rate limit.
package adsb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/avanha/fullflight/internal/version"
)

type Source struct {
	Name    string
	BaseURL string // e.g. https://api.adsb.lol/v2
	RegPath string // "reg" (adsb.lol) or "registration" (adsb.fi)
	// FallbackOnly keeps a source out of the rotation (see Client.Spread): it is only asked after the others.
	FallbackOnly bool
	// APIKey, if set, is sent with every request to this source in the APIKeyHeader header. adsb.lol gives a key to feeders and
	// on request; the API answers without one today.
	APIKey string
}

// APIKeyHeader is where adsb.lol looks for a key, as in the curl example it gives with a key (header names are not case sensitive).
// A key it does not accept is answered 401 "invalid api key".
const APIKeyHeader = "X-API-Key"

var (
	ADSBLol = Source{Name: "adsb.lol", BaseURL: "https://api.adsb.lol/v2", RegPath: "reg"}
	ADSBFi  = Source{Name: "adsb.fi", BaseURL: "https://opendata.adsb.fi/api/v2", RegPath: "registration"}
	// AirplanesLive answers the same queries (GET /v2/hex/{hex}, /callsign/{cs}, /reg/{reg}, /point/...). Its
	// operators have asked to be contacted for access (it has answered 403 to requests without it), so it is
	// only used when named in the configuration.
	AirplanesLive = Source{Name: "airplanes.live", BaseURL: "https://api.airplanes.live/v2", RegPath: "reg", FallbackOnly: true}
)

// A source that refuses us is left alone for a while rather than asked again with every poll.
const (
	refusedFor = time.Hour        // HTTP 401 or 403: we are not allowed
	tooManyFor = 30 * time.Second // HTTP 429: slow down; doubled for each 429 in a row, up to refusedFor
	// maxRetryAfter is the longest a 429's Retry-After is honoured. adsb.lol's keys have day, week and month allowances and say
	// when each lifts, and asking again sooner is a further rate-limit hit, which it says can lose the key.
	maxRetryAfter = 7 * 24 * time.Hour
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

// OnGround reports whether the aircraft is on the ground: the transponder says
// so, or it is moving too slowly to be flying.
func (a Aircraft) OnGround() bool {
	return a.AltBaro.OnGround || (a.GS != nil && *a.GS < 30)
}

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

// Client queries its sources in order. An empty answer is not taken as final: sources see
// different parts of the sky (an aircraft adsb.lol has never heard may be on adsb.fi, and the
// reverse), and a position without a callsign is not the whole story either. So it keeps asking
// until it has an answer that is complete (every aircraft has a position and a callsign) or the
// sources run out, then merges what it got. An error from one source is skipped as long as another
// answers.
type Client struct {
	Sources    []Source
	HTTP       *http.Client
	MinSpacing time.Duration // between requests to the same source; default 1.25s, a little over the second adsb.fi allows
	UserAgent  string
	// Spread rotates which source is asked first, one query after another, instead of always starting with the
	// first in the list: each carries a share of the load, and the others still back it up. Sources marked
	// FallbackOnly stay at the end.
	Spread bool

	Now func() time.Time // for tests; default time.Now
	// OnPause, if set, is told when a source is paused: which, the HTTP status that caused it, and until when.
	OnPause func(source string, status int, until time.Time)

	turn    atomic.Uint32 // counts queries, to rotate the first source
	mu      sync.Mutex
	next    map[string]time.Time
	blocked map[string]pause // source name -> why and until when
	strikes map[string]int   // source name -> 429s in a row, which lengthen the pause
}

type pause struct {
	until  time.Time
	status int
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// block leaves a source alone for d, because it answered with status.
func (c *Client) block(name string, status int, d time.Duration) {
	c.mu.Lock()
	if c.blocked == nil {
		c.blocked = map[string]pause{}
	}
	until := c.now().Add(d)
	already := c.blocked[name].until.After(c.now())
	c.blocked[name] = pause{until, status}
	c.mu.Unlock()
	if !already && c.OnPause != nil {
		c.OnPause(name, status, until)
	}
}

func NewClient(sources ...Source) *Client {
	if len(sources) == 0 {
		sources = []Source{ADSBLol, ADSBFi}
	}
	return &Client{Sources: sources, UserAgent: version.UserAgent(), HTTP: &http.Client{Timeout: 15 * time.Second,
		// a redirect to another host does not take a key with it (Go forwards custom headers, only dropping Authorization and cookies)
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del(APIKeyHeader)
			}
			return nil
		}}}
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

// order is the sources in the order to ask them for one query.
func (c *Client) order() []Source {
	if !c.Spread || len(c.Sources) < 2 {
		return c.Sources
	}
	var front, back []Source
	for _, s := range c.Sources {
		if s.FallbackOnly {
			back = append(back, s)
		} else {
			front = append(front, s)
		}
	}
	if len(front) > 1 {
		k := int(c.turn.Add(1)-1) % len(front)
		front = append(append([]Source{}, front[k:]...), front[:k]...)
	}
	return append(front, back...)
}

func (c *Client) query(ctx context.Context, path func(Source) string) ([]Aircraft, error) {
	var errs []error
	var merged []Aircraft
	answered := false
	for _, s := range c.order() {
		ac, err := c.get(ctx, s, path(s))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		answered = true
		for i := range ac {
			ac[i].Source = s.Name
			ac[i].Hex = strings.ToUpper(ac[i].Hex)
		}
		merged = merge(merged, ac)
		if complete(merged) {
			break
		}
	}
	if !answered {
		return nil, errors.Join(errs...)
	}
	return merged, nil
}

// FreshEnough is how old a position may be (seconds since it was last updated) before another
// source is asked for a fresher one. The poller discards positions over a minute old.
const FreshEnough = 20.0

// complete reports whether every aircraft has a recent position and a callsign, so asking
// another source could add little.
func complete(ac []Aircraft) bool {
	if len(ac) == 0 {
		return false
	}
	for _, a := range ac {
		if a.Lat == nil || a.Lon == nil || strings.TrimSpace(a.Flight) == "" || (a.SeenPos != nil && *a.SeenPos > FreshEnough) {
			return false
		}
	}
	return true
}

// merge combines two sources' answers. An aircraft both report keeps the fresher position, and
// anything it lacks (callsign, registration, type, squawk) is filled in from the other.
func merge(have, more []Aircraft) []Aircraft {
	for _, b := range more {
		k := -1
		for i := range have {
			if have[i].Hex == b.Hex {
				k = i
				break
			}
		}
		if k < 0 {
			have = append(have, b)
			continue
		}
		a := have[k]
		if fresher(b, a) {
			a, b = b, a
		}
		if strings.TrimSpace(a.Flight) == "" {
			a.Flight = b.Flight
		}
		if a.Reg == "" {
			a.Reg = b.Reg
		}
		if a.Type == "" {
			a.Type = b.Type
		}
		if a.Squawk == "" {
			a.Squawk = b.Squawk
		}
		have[k] = a
	}
	return have
}

// fresher reports whether x has a newer position than y.
func fresher(x, y Aircraft) bool {
	if x.Lat == nil || x.Lon == nil {
		return false
	}
	if y.Lat == nil || y.Lon == nil {
		return true
	}
	return x.SeenPos != nil && y.SeenPos != nil && *x.SeenPos < *y.SeenPos
}

func (c *Client) get(ctx context.Context, s Source, path string) ([]Aircraft, error) {
	c.mu.Lock()
	p := c.blocked[s.Name]
	c.mu.Unlock()
	if c.now().Before(p.until) {
		return nil, fmt.Errorf("paused (HTTP %d) until %s", p.status, p.until.UTC().Format("15:04:05Z"))
	}
	if err := c.wait(ctx, s.Name); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if s.APIKey != "" {
		req.Header.Set(APIKeyHeader, s.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			c.block(s.Name, resp.StatusCode, refusedFor)
		case http.StatusTooManyRequests:
			d := c.backoff(s.Name)
			if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
				d = min(max(time.Duration(n)*time.Second, d), maxRetryAfter)
			}
			// a limit over a whole day (a key's allowance used up) will not lift in half a minute: no point asking again soon
			if body, _ := io.ReadAll(io.LimitReader(resp.Body, 512)); dailyLimit(string(body)) {
				d = refusedFor
			}
			c.block(s.Name, resp.StatusCode, d)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("bad JSON: %w", err)
	}
	c.mu.Lock()
	delete(c.strikes, s.Name) // answered: the next 429 starts again at the short pause
	c.mu.Unlock()
	return r.AC, nil
}

// backoff is how long to leave a source alone after a 429: tooManyFor, and twice as long for each one in a row, up to refusedFor.
func (c *Client) backoff(name string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.strikes == nil {
		c.strikes = map[string]int{}
	}
	n := c.strikes[name]
	c.strikes[name] = n + 1
	if n >= 7 { // 30 s doubled seven times is past the hour; stop before the shift grows
		return refusedFor
	}
	return min(tooManyFor<<n, refusedFor)
}

// dailyLimit reports whether a 429's body says the limit is over a day ("rate limit exceeded: 1d requests window").
func dailyLimit(body string) bool {
	b := strings.ToLower(body)
	return strings.Contains(b, "1d requests") || strings.Contains(b, "daily") || strings.Contains(b, "per day")
}

// wait reserves the next request slot for a source, sleeping if needed.
func (c *Client) wait(ctx context.Context, name string) error {
	spacing := c.MinSpacing
	if spacing == 0 {
		spacing = 1250 * time.Millisecond
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
