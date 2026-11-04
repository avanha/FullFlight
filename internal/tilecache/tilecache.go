// Package tilecache serves OpenStreetMap's map tiles from a directory on this machine, fetching a tile from OSM only when
// someone's browser asks for one that is not held (or has expired). Several browsers on one server then cost OSM one request
// per tile, not one each. It follows OSM's tile usage policy for a caching proxy: a User-Agent that names FullFlight, the
// tile's own cache headers honoured (or seven days when it names none), a conditional request once a tile has expired, and
// never a tile nobody asked for.
package tilecache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// OSM is the tile server.
	OSM = "https://tile.openstreetmap.org"
	// MaxZoom is the finest zoom the map asks for.
	MaxZoom = 18

	defaultTTL   = 7 * 24 * time.Hour // OSM's minimum when a tile names no lifetime
	minTTL       = time.Hour
	maxTTL       = 30 * 24 * time.Hour
	maxTileBytes = 1 << 20
	holdFor      = 10 * time.Minute
	// browserMaxAge is how long a browser may keep a tile without asking us.
	browserMaxAge = 24 * time.Hour
)

// Cache is a directory of tiles: Dir/<z>/<x>/<y>.png, each with a Dir/<z>/<x>/<y>.meta saying when it expires.
type Cache struct {
	Dir       string
	MaxBytes  int64 // the oldest tiles are deleted beyond this; 0 for no limit
	Upstream  string
	UserAgent string // names FullFlight to OSM
	Referer   string // where the app is described: a server has no page of its own to name
	HTTP      *http.Client
	Log       *slog.Logger
	Now       func() time.Time

	mu      sync.Mutex
	calls   map[string]*call
	size    int64
	counted bool
	until   time.Time // OSM asked us to stop until then
	sem     chan struct{}
}

type call struct {
	done chan struct{}
	err  error
}

type meta struct {
	Expires      int64  `json:"expires"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Cache) upstream() string {
	if c.Upstream != "" {
		return c.Upstream
	}
	return OSM
}

func (c *Cache) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Valid says whether z/x/y names a tile that exists.
func Valid(z, x, y int) bool {
	return z >= 0 && z <= MaxZoom && x >= 0 && y >= 0 && x < 1<<z && y < 1<<z
}

func (c *Cache) paths(z, x, y int) (png, mt string) {
	base := filepath.Join(c.Dir, strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y))
	return base + ".png", base + ".meta"
}

// Serve answers a request for a tile from the cache, filling it from OSM first when it is missing or has expired.
func (c *Cache) Serve(w http.ResponseWriter, r *http.Request, z, x, y int) {
	if !Valid(z, x, y) {
		http.NotFound(w, r)
		return
	}
	body, err := c.tile(z, x, y)
	switch {
	case errors.Is(err, errNoTile):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, "map tile unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(browserMaxAge/time.Second)))
	w.Write(body)
}

var errNoTile = errors.New("no such tile")

// tile returns the tile's image, from the directory when it is there and fresh. An expired one is asked about again, and kept
// if OSM cannot answer: an old map beats none.
func (c *Cache) tile(z, x, y int) ([]byte, error) {
	png, mt := c.paths(z, x, y)
	body, readErr := os.ReadFile(png)
	m, _ := readMeta(mt)
	held := readErr == nil
	if held && c.now().Unix() < m.Expires {
		return body, nil
	}
	err := c.fill(z, x, y, m, held)
	if err == nil {
		return os.ReadFile(png)
	}
	if held && !errors.Is(err, errNoTile) {
		c.log().Warn("map tile not refreshed; serving the old one", "tile", fmt.Sprintf("%d/%d/%d", z, x, y), "err", err)
		return body, nil
	}
	return nil, err
}

func readMeta(path string) (meta, error) {
	var m meta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// fill fetches the tile, once however many ask at the same moment. When it is held (stale), the request is conditional.
func (c *Cache) fill(z, x, y int, held meta, stale bool) error {
	key := fmt.Sprintf("%d/%d/%d", z, x, y)
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]*call{}
		c.sem = make(chan struct{}, 2) // OSM asks for no more than two at a time from one client
	}
	if cl, ok := c.calls[key]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.calls[key] = cl
	c.mu.Unlock()

	cl.err = c.fetch(z, x, y, held, stale)
	c.mu.Lock()
	delete(c.calls, key)
	c.mu.Unlock()
	close(cl.done)
	return cl.err
}

func (c *Cache) fetch(z, x, y int, held meta, stale bool) error {
	c.mu.Lock()
	wait := c.until.Sub(c.now())
	c.mu.Unlock()
	if wait > 0 {
		return fmt.Errorf("OSM asked us to stop for a while (%s more)", wait.Round(time.Minute))
	}
	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%d/%d/%d.png", c.upstream(), z, x, y), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if c.Referer != "" {
		req.Header.Set("Referer", c.Referer)
	}
	if stale { // a conditional request, as OSM asks of a cache whose tile has expired
		if held.ETag != "" {
			req.Header.Set("If-None-Match", held.ETag)
		}
		if held.LastModified != "" {
			req.Header.Set("If-Modified-Since", held.LastModified)
		}
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	png, mt := c.paths(z, x, y)
	switch {
	case resp.StatusCode == http.StatusNotModified && stale:
		return c.writeMeta(mt, freshness(resp, held, c.now()))
	case resp.StatusCode == http.StatusNotFound:
		return errNoTile
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500:
		c.mu.Lock()
		c.until = c.now().Add(holdFor)
		c.mu.Unlock()
		return fmt.Errorf("OSM answered HTTP %d; leaving it alone for %s", resp.StatusCode, holdFor)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("OSM answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTileBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxTileBytes || !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		return errors.New("OSM sent something that is not a tile")
	}
	if err := os.MkdirAll(filepath.Dir(png), 0o700); err != nil {
		return err
	}
	tmp := png + ".part"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, png); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := c.writeMeta(mt, freshness(resp, meta{}, c.now())); err != nil {
		return err
	}
	c.added(int64(len(body)))
	return nil
}

// freshness reads a response's own cache headers: Cache-Control max-age, else Expires, else seven days. A 304 that says
// nothing keeps the validators already held.
func freshness(resp *http.Response, was meta, now time.Time) meta {
	ttl := defaultTTL
	if cc := resp.Header.Get("Cache-Control"); cc != "" {
		for _, d := range strings.Split(cc, ",") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(d), "max-age="); ok {
				if n, err := strconv.Atoi(v); err == nil {
					ttl = time.Duration(n) * time.Second
				}
			}
		}
	} else if e, err := http.ParseTime(resp.Header.Get("Expires")); err == nil {
		if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			ttl = e.Sub(d)
		} else {
			ttl = e.Sub(now)
		}
	}
	ttl = max(minTTL, min(maxTTL, ttl))
	m := meta{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	if m.ETag == "" {
		m.ETag = was.ETag
	}
	if m.LastModified == "" {
		m.LastModified = was.LastModified
	}
	m.Expires = now.Add(ttl).Unix()
	return m
}

func (c *Cache) writeMeta(path string, m meta) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(path, b, 0o600)
}

// added counts a tile written and deletes the oldest tiles if the directory has grown past its limit.
func (c *Cache) added(n int64) {
	if c.MaxBytes <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.counted {
		c.size, c.counted = c.walk(nil), true
	}
	c.size += n
	if c.size <= c.MaxBytes {
		return
	}
	var tiles []tileFile
	c.walk(&tiles)
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].t.Before(tiles[j].t) })
	for _, f := range tiles {
		if c.size <= c.MaxBytes*9/10 { // some room, so it is not at the edge on every tile
			break
		}
		os.Remove(f.path)
		os.Remove(strings.TrimSuffix(f.path, ".png") + ".meta")
		c.size -= f.bytes
	}
}

type tileFile struct {
	path  string
	bytes int64
	t     time.Time
}

// walk totals the tile files, and lists them when asked.
func (c *Cache) walk(out *[]tileFile) (total int64) {
	filepath.WalkDir(c.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".png") {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
			if out != nil {
				*out = append(*out, tileFile{p, fi.Size(), fi.ModTime()})
			}
		}
		return nil
	})
	return
}
