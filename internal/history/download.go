package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is where releases are looked up. The archive of year Y is the repository adsblol/globe_history_Y, and a day is a
// release tagged v2026.10.31-planes-readsb-prod-0 whose assets are the parts of a tar.
const DefaultAPI = "https://api.github.com"

// Fetcher downloads a day's release into a Cache.
type Fetcher struct {
	Client      *http.Client // default: no overall timeout, because a day is gigabytes
	API         string       // default DefaultAPI
	UserAgent   string
	Log         *slog.Logger
	IdleTimeout time.Duration // give up on a download that has sent nothing for this long; default 2m
}

// Asset is one part of a release.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Release is a day's release, with only its tar parts.
type Release struct {
	Tag    string
	Assets []Asset // in order: .aa, .ab, ...
}

// Bytes is the size of all the parts.
func (r Release) Bytes() (n int64) {
	for _, a := range r.Assets {
		n += a.Size
	}
	return n
}

// ErrNotPublished says neither the prod nor the staging release of a day exists (yet).
var ErrNotPublished = errors.New("no release for that day")

var assetName = regexp.MustCompile(`^v\d{4}\.\d\d\.\d\d-planes-readsb-(?:prod|staging)-0\.tar\.([a-z]{2})$`)

func (f *Fetcher) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute}}
}

func (f *Fetcher) api() string {
	if f.API != "" {
		return f.API
	}
	return DefaultAPI
}

func (f *Fetcher) request(ctx context.Context, url string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return f.client().Do(req)
}

// snippet is the start of a response body, for the log.
func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 300))
	return strings.Join(strings.Fields(string(b)), " ")
}

// Release looks up the release of day (YYYY-MM-DD): the prod one, or the staging one if there is no prod. Each failure is
// logged with what came back and a hint at what to check, because the layout is adsb.lol's to change.
func (f *Fetcher) Release(ctx context.Context, day string) (Release, error) {
	dots := strings.ReplaceAll(day, "-", ".")
	if len(dots) != 10 {
		return Release{}, fmt.Errorf("day %q: want YYYY-MM-DD", day)
	}
	repo := "globe_history_" + dots[:4]
	unusable := false // a release was there, but not as we expect
	for _, kind := range kinds {
		tag := "v" + dots + "-planes-readsb-" + kind + "-0"
		url := f.api() + "/repos/adsblol/" + repo + "/releases/tags/" + tag
		resp, err := f.request(ctx, url, map[string]string{"Accept": "application/vnd.github+json"})
		if err != nil {
			f.log().Error("backfill: could not reach the release list", "url", url, "err", err,
				"hint", "network down, or api.github.com unreachable from here")
			return Release{}, err
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			f.log().Info("backfill: no such release", "tag", tag)
			continue
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
			body := snippet(resp.Body)
			resp.Body.Close()
			f.log().Error("backfill: GitHub refused the release lookup", "url", url, "status", resp.StatusCode,
				"ratelimit_remaining", resp.Header.Get("X-RateLimit-Remaining"), "ratelimit_reset", resetTime(resp.Header.Get("X-RateLimit-Reset")),
				"body", body, "hint", "the unauthenticated GitHub API allows 60 requests an hour per address; wait until the reset time")
			return Release{}, fmt.Errorf("GitHub refused the lookup (%d); try again after %s", resp.StatusCode, resetTime(resp.Header.Get("X-RateLimit-Reset")))
		case resp.StatusCode != http.StatusOK:
			body := snippet(resp.Body)
			resp.Body.Close()
			f.log().Error("backfill: unexpected answer to the release lookup", "url", url, "status", resp.StatusCode, "body", body,
				"hint", "GitHub's API may have changed, or the repository moved: check the URL in a browser")
			return Release{}, fmt.Errorf("release lookup answered %d", resp.StatusCode)
		}
		var body struct {
			Tag    string  `json:"tag_name"`
			Assets []Asset `json:"assets"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			f.log().Error("backfill: the release lookup was not the JSON expected", "url", url, "err", err,
				"hint", "GitHub's release API shape has changed: expected {tag_name, assets:[{name,size,browser_download_url}]}")
			return Release{}, fmt.Errorf("release lookup: %w", err)
		}
		rel := Release{Tag: tag}
		var names []string
		for _, a := range body.Assets {
			names = append(names, a.Name)
			if assetName.MatchString(a.Name) && a.Size > 0 && a.URL != "" {
				rel.Assets = append(rel.Assets, a)
			}
		}
		sort.Slice(rel.Assets, func(i, j int) bool { return rel.Assets[i].Name < rel.Assets[j].Name })
		for i, a := range rel.Assets {
			if !strings.HasSuffix(a.Name, ".tar."+partSuffix(i)) {
				f.log().Error("backfill: the parts of the release do not run aa, ab, ... without a gap", "tag", tag, "assets", names,
					"hint", "adsb.lol may have changed how a day is split; Parts and the downloader expect <tag>.tar.aa, .ab, ...")
				return Release{}, fmt.Errorf("release %s: part %s is missing", tag, partSuffix(i))
			}
		}
		if len(rel.Assets) == 0 {
			f.log().Warn("backfill: the release has no tar parts we recognise", "tag", tag, "assets", names,
				"hint", "adsb.lol may have renamed the files; expected v<date>-planes-readsb-prod-0.tar.aa and so on")
			unusable = true
			continue
		}
		f.log().Info("backfill: found the release", "tag", rel.Tag, "parts", len(rel.Assets), "bytes", rel.Bytes())
		return rel, nil
	}
	if unusable {
		return Release{}, fmt.Errorf("%s: the release exists but its files are not the ones expected (see the log for their names)", day)
	}
	f.log().Warn("backfill: no usable release for the day", "day", day, "repo", repo,
		"hint", "a day is published about 03:30Z the next day; if it should be there, compare the tag naming at https://github.com/adsblol/"+repo+"/releases")
	return Release{}, fmt.Errorf("%s: %w", day, ErrNotPublished)
}

func resetTime(unix string) string {
	if n, err := strconv.ParseInt(unix, 10, 64); err == nil {
		return time.Unix(n, 0).UTC().Format("15:04Z")
	}
	return "later"
}

// Download fetches the parts of rel into c, resuming a part left half done. progress is told how many bytes of the day have
// arrived so far, and the total. Parts are written as name.part and renamed when complete and of the size GitHub gave, so
// anything without .part in the cache is whole.
func (f *Fetcher) Download(ctx context.Context, c Cache, rel Release, progress func(done, total int64)) error {
	dir := filepath.Join(c.Dir, "adsblol")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.log().Error("backfill: cannot write to the cache directory", "dir", dir, "err", err,
			"hint", "is the volume mounted and writable? backfill.cache_dir must exist")
		return err
	}
	total := rel.Bytes()
	began := time.Now()
	var done, fetched, nextLog int64
	add := func(n int64) {
		done += n
		if progress != nil {
			progress(done, total)
		}
		if done >= nextLog {
			f.log().Info("backfill: downloading", "tag", rel.Tag, "done_bytes", done, "total_bytes", total, "percent", 100*done/total)
			nextLog = (100*done/total/10 + 1) * total / 10
		}
	}
	f.log().Info("backfill: download starts", "tag", rel.Tag, "parts", len(rel.Assets), "bytes", total, "dir", dir)
	for _, a := range rel.Assets {
		dest := filepath.Join(dir, a.Name)
		if st, err := os.Stat(dest); err == nil && st.Size() == a.Size {
			f.log().Info("backfill: part already in the cache", "part", a.Name)
			add(a.Size)
			continue
		}
		n, err := f.fetchPart(ctx, a, dest, add)
		fetched += n
		if err != nil {
			return err
		}
	}
	el := time.Since(began)
	f.log().Info("backfill: download complete", "tag", rel.Tag, "bytes", total, "fetched_bytes", fetched, "took", el.Round(time.Second),
		"mb_per_s", fmt.Sprintf("%.1f", float64(fetched)/1e6/el.Seconds()))
	return nil
}

// fetchPart gets one part, returning how many bytes came over the network.
func (f *Fetcher) fetchPart(ctx context.Context, a Asset, dest string, add func(int64)) (int64, error) {
	tmp := dest + ".part"
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}
	if have > a.Size {
		f.log().Warn("backfill: a half-downloaded part is larger than the release says; starting it again", "part", a.Name, "have", have, "want", a.Size)
		os.Remove(tmp)
		have = 0
	}
	idle := f.IdleTimeout
	if idle == 0 {
		idle = 2 * time.Minute
	}
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	watchdog := time.AfterFunc(idle, cancel)
	defer watchdog.Stop()

	var n int64
	if have < a.Size {
		hdr := map[string]string{}
		if have > 0 {
			hdr["Range"] = fmt.Sprintf("bytes=%d-", have)
			f.log().Info("backfill: resuming a part", "part", a.Name, "have", have, "of", a.Size)
		}
		resp, err := f.request(ctx2, a.URL, hdr)
		if err != nil {
			return 0, f.downloadErr(ctx, ctx2, a, "could not start the download", err, idle)
		}
		defer resp.Body.Close()
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		switch {
		case resp.StatusCode == http.StatusPartialContent && have > 0:
			if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", have)) {
				f.log().Error("backfill: the server resumed from somewhere else", "part", a.Name, "asked_from", have, "content_range", resp.Header.Get("Content-Range"),
					"hint", "delete the .part file and try again")
				return 0, fmt.Errorf("%s: resumed from the wrong place", a.Name)
			}
			add(have)
		case resp.StatusCode == http.StatusOK:
			if have > 0 {
				f.log().Warn("backfill: the server ignored the resume request; starting the part again", "part", a.Name,
					"hint", "the download host may have stopped supporting Range requests")
			}
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		default:
			f.log().Error("backfill: the download was refused", "part", a.Name, "url", a.URL, "status", resp.StatusCode, "body", snippet(resp.Body),
				"location", resp.Header.Get("Location"),
				"hint", "the asset may have been replaced or removed, or the link scheme changed: check the release page; the .part file is kept for a retry")
			return 0, fmt.Errorf("%s: download answered %d", a.Name, resp.StatusCode)
		}
		out, err := os.OpenFile(tmp, flags, 0o644)
		if err != nil {
			f.log().Error("backfill: cannot write the part", "file", tmp, "err", err, "hint", "disk full or the volume unmounted?")
			return 0, err
		}
		n, err = io.Copy(out, &stall{r: resp.Body, t: watchdog, d: idle, add: add})
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, f.downloadErr(ctx, ctx2, a, "the download broke off", err, idle)
		}
	} else {
		add(a.Size)
	}
	st, err := os.Stat(tmp)
	if err != nil || st.Size() != a.Size {
		got := int64(-1)
		if err == nil {
			got = st.Size()
		}
		f.log().Error("backfill: the part is not the size the release gave", "part", a.Name, "want", a.Size, "got", got,
			"hint", "the connection ended early; run the backfill again to resume")
		return n, fmt.Errorf("%s: got %d of %d bytes", a.Name, got, a.Size)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return n, err
	}
	f.log().Info("backfill: part complete", "part", a.Name, "bytes", a.Size)
	return n, nil
}

func (f *Fetcher) downloadErr(parent, child context.Context, a Asset, what string, err error, idle time.Duration) error {
	switch {
	case parent.Err() != nil:
		f.log().Info("backfill: download cancelled; the partial file is kept to resume", "part", a.Name)
		return parent.Err()
	case child.Err() != nil:
		f.log().Error("backfill: the download stalled", "part", a.Name, "idle", idle, "hint", "no data arrived; the partial file is kept, so starting again resumes")
		return fmt.Errorf("%s: no data for %s", a.Name, idle)
	}
	f.log().Error("backfill: "+what, "part", a.Name, "url", a.URL, "err", err,
		"hint", "network trouble or the host closed the connection; the partial file is kept, so starting again resumes")
	return fmt.Errorf("%s: %w", a.Name, err)
}

// stall passes reads through, counting them and pushing the idle deadline back.
type stall struct {
	r   io.Reader
	t   *time.Timer
	d   time.Duration
	add func(int64)
}

func (s *stall) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.t.Reset(s.d)
		s.add(int64(n))
	}
	return n, err
}
