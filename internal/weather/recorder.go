package weather

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

const (
	// IndexURL is RainViewer's list of radar frames.
	IndexURL = "https://api.rainviewer.com/public/weather-maps.json"
	// HoldName is how a refusal is kept in the store, so that a restart does not ask again at once.
	HoldName = "rainviewer"

	maxTileBytes = 1 << 20
	// maxFixGap is the longest silence a position is interpolated across to place an aircraft in a frame's window.
	maxFixGap = time.Hour
	holdFor   = time.Hour
)

// Recorder keeps the radar tiles around the aircraft being followed. RainViewer lists only the last two hours of frames, so
// it must come back at least that often; each visit takes the frames Step apart and, for each, the tiles within Ring of where
// the aircraft were during the minutes that frame stands for.
type Recorder struct {
	Store     *store.Store
	Dir       string        // where the tile files go: Dir/radar/<frame unix time>/<z>/<x>/<y>.png
	HTTP      *http.Client  // default: 30 s timeout
	UserAgent string        // names FullFlight to RainViewer
	Step      time.Duration // between recorded frames; default 20 minutes
	Ring      int           // tiles kept around the aircraft's path; default 2
	MaxBytes  int64         // oldest frames are deleted beyond this; 0 for no limit
	Pause     time.Duration // between tile requests; default 250 ms
	IndexURL  string        // default IndexURL
	Host      string        // overrides the index's host (tests); otherwise it must be RainViewer's
	Log       *slog.Logger
	Now       func() time.Time
}

// Result says what a visit did.
type Result struct {
	Frames  int   // frames in the window that had an aircraft in them
	Fetched int   // tiles fetched
	Had     int   // tiles already held
	Bytes   int64 // what the fetched tiles weigh
	Evicted int   // frames deleted to stay under the limit
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Recorder) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Recorder) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (r *Recorder) step() time.Duration {
	if r.Step > 0 {
		return r.Step
	}
	return 20 * time.Minute
}

func (r *Recorder) ring() int {
	if r.Ring > 0 {
		return r.Ring
	}
	return 2
}

func (r *Recorder) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if r.UserAgent != "" {
		req.Header.Set("User-Agent", r.UserAgent)
	}
	return r.client().Do(req)
}

// refused keeps a "not now" from RainViewer (too many requests, or it is struggling) and says so.
func (r *Recorder) refused(status int, now time.Time) error {
	until := now.Add(holdFor)
	r.Store.SetProviderHold(HoldName, until, "HTTP "+strconv.Itoa(status))
	return fmt.Errorf("RainViewer answered HTTP %d; leaving it alone until %s", status, until.UTC().Format("15:04Z"))
}

// RecordOnce makes one visit: reads the index, and fetches the tiles it does not yet hold.
func (r *Recorder) RecordOnce(ctx context.Context) (Result, error) {
	var res Result
	now := r.now()
	if until, why, ok := r.Store.ProviderHold(HoldName, now); ok {
		return res, fmt.Errorf("not asking RainViewer until %s (%s)", until.UTC().Format("15:04Z"), why)
	}
	u := r.IndexURL
	if u == "" {
		u = IndexURL
	}
	resp, err := r.get(ctx, u)
	if err != nil {
		return res, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return res, r.refused(resp.StatusCode, now)
		}
		return res, fmt.Errorf("the radar index: HTTP %d", resp.StatusCode)
	}
	ix, err := ParseIndex(resp.Body)
	resp.Body.Close()
	if err != nil {
		return res, fmt.Errorf("the radar index: %w", err)
	}
	if r.Host != "" {
		ix.Host = r.Host
	} else if err := CheckHost(ix); err != nil {
		return res, err
	}
	frames := ix.Every(r.step())
	if len(frames) == 0 {
		return res, nil
	}

	// where each followed aircraft was, over the span the frames cover (plus what interpolating needs either side)
	from, to := frames[0].Time.Add(-r.step()/2-maxFixGap), frames[len(frames)-1].Time.Add(r.step()/2+maxFixGap)
	paths, err := r.paths(from, to)
	if err != nil {
		return res, err
	}
	for _, f := range frames {
		var tiles []Tile
		seen := map[Tile]bool{}
		for _, fixes := range paths {
			for _, t := range TilesAlong(fixes, f.Time.Add(-r.step()/2), f.Time.Add(r.step()/2), RadarZoom, r.ring(), maxFixGap) {
				if !seen[t] {
					seen[t] = true
					tiles = append(tiles, t)
				}
			}
		}
		if len(tiles) == 0 {
			continue
		}
		res.Frames++
		for _, t := range tiles {
			if r.Store.HaveWxTile(SourceRadar, f.Time, t.Z, t.X, t.Y) {
				res.Had++
				continue
			}
			n, err := r.fetchTile(ctx, ix.Host, f, t)
			if err != nil {
				r.evict(&res)
				return res, err
			}
			if n > 0 {
				res.Fetched++
				res.Bytes += n
			}
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(r.pause()):
			}
		}
	}
	r.evict(&res)
	return res, nil
}

func (r *Recorder) pause() time.Duration {
	if r.Pause > 0 {
		return r.Pause
	}
	return 250 * time.Millisecond
}

// paths returns each followed aircraft's positions in the span: those of every watch that has an aircraft.
func (r *Recorder) paths(from, to time.Time) ([][]Point, error) {
	watches, err := r.Store.ListWatches(false)
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	var out [][]Point
	for _, w := range watches {
		if w.Hex == "" || done[w.Hex] {
			continue
		}
		done[w.Hex] = true
		fixes, err := r.Store.AirFixes(w.Hex, from, to)
		if err != nil {
			return nil, err
		}
		var pts []Point
		for _, f := range fixes {
			if !f.OnGround {
				pts = append(pts, Point{f.T, f.Lat, f.Lon})
			}
		}
		if len(pts) > 0 {
			out = append(out, pts)
		}
	}
	return out, nil
}

// fetchTile downloads one tile and records it, returning its size; a tile RainViewer does not have (404) is skipped, and
// returns 0. Any other refusal ends the visit.
func (r *Recorder) fetchTile(ctx context.Context, host string, f Frame, t Tile) (int64, error) {
	resp, err := r.get(ctx, TileURL(host, f, t))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return 0, r.refused(resp.StatusCode, r.now())
	case resp.StatusCode != http.StatusOK:
		return 0, fmt.Errorf("radar tile %d/%d/%d: HTTP %d", t.Z, t.X, t.Y, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTileBytes+1))
	if err != nil {
		return 0, err
	}
	if len(body) > maxTileBytes || !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		return 0, fmt.Errorf("radar tile %d/%d/%d is not a PNG image of a sensible size", t.Z, t.X, t.Y)
	}
	path := r.TilePath(f.Time, t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	err = r.Store.AddWxTile(store.WxTile{Source: SourceRadar, Frame: f.Time, Z: t.Z, X: t.X, Y: t.Y, Bytes: int64(len(body)), Fetched: r.now()})
	return int64(len(body)), err
}

// TilePath is where a recorded radar tile's file is.
func (r *Recorder) TilePath(frame time.Time, t Tile) string { return TilePath(r.Dir, frame, t) }

// TilePath is where a recorded radar tile's file is, in a weather directory.
func TilePath(dir string, frame time.Time, t Tile) string {
	return filepath.Join(dir, SourceRadar, strconv.FormatInt(frame.Unix(), 10), strconv.Itoa(t.Z), strconv.Itoa(t.X), strconv.Itoa(t.Y)+".png")
}

// evict deletes the oldest frames while the tiles weigh more than MaxBytes.
func (r *Recorder) evict(res *Result) {
	if r.MaxBytes <= 0 {
		return
	}
	for {
		n, err := r.Store.WxBytes()
		if err != nil || n <= r.MaxBytes {
			return
		}
		src, frame, ok := r.Store.OldestWxFrame()
		if !ok {
			return
		}
		tiles, err := r.Store.WxFrameTiles(src, frame)
		if err != nil {
			return
		}
		for _, t := range tiles {
			os.Remove(r.TilePath(frame, Tile{t.Z, t.X, t.Y}))
		}
		os.RemoveAll(filepath.Join(r.Dir, src, strconv.FormatInt(frame.Unix(), 10))) // its now empty folders
		if err := r.Store.DeleteWxFrame(src, frame); err != nil {
			return
		}
		res.Evicted++
	}
}

// Run makes a visit soon after it starts and then every interval, until ctx ends. A visit that fails is logged, and the next
// one is the next scheduled.
func (r *Recorder) Run(ctx context.Context, every time.Duration) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		res, err := r.RecordOnce(ctx)
		if err != nil {
			r.log().Warn("recording radar", "err", err, "fetched", res.Fetched)
		} else if res.Fetched > 0 || res.Evicted > 0 {
			r.log().Info("recorded radar", "frames", res.Frames, "tiles", res.Fetched, "already_held", res.Had, "bytes", res.Bytes, "evicted_frames", res.Evicted)
		}
		wait = every
	}
}
