package tilecache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const png = "\x89PNG\r\n\x1a\n-a tile-"

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// rig is an OSM that counts what it is asked, in front of a cache with a clock the test moves.
type rig struct {
	t       *testing.T
	c       *Cache
	srv     *httptest.Server
	asked   atomic.Int32
	handler func(w http.ResponseWriter, r *http.Request)
	mu      sync.Mutex
	now     time.Time
	last    *http.Request
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, now: t0}
	r.handler = func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(png))
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.asked.Add(1)
		r.mu.Lock()
		r.last = q.Clone(q.Context())
		r.mu.Unlock()
		r.handler(w, q)
	}))
	t.Cleanup(r.srv.Close)
	r.c = &Cache{Dir: t.TempDir(), Upstream: r.srv.URL, UserAgent: "FullFlight/test (+https://example.test)", Referer: "https://example.test/",
		Now: func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }}
	return r
}

func (r *rig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

func (r *rig) get(z, x, y int) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.c.Serve(w, httptest.NewRequest("GET", "/tiles/osm/x", nil), z, x, y)
	return w
}

func TestAFirstRequestFetchesAndTheNextIsServedLocally(t *testing.T) {
	r := newRig(t)
	w := r.get(3, 2, 5)
	if w.Code != 200 || w.Body.String() != png || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Header().Get("Cache-Control"), "public, max-age=") {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if r.last.URL.Path != "/3/2/5.png" || r.last.Header.Get("User-Agent") != "FullFlight/test (+https://example.test)" || r.last.Header.Get("Referer") != "https://example.test/" {
		t.Errorf("OSM was asked %s with %v", r.last.URL.Path, r.last.Header)
	}
	if r.last.Header.Get("Cache-Control") != "" || r.last.Header.Get("Pragma") != "" {
		t.Error("a no-cache header was sent to OSM")
	}
	for i := 0; i < 3; i++ {
		if w := r.get(3, 2, 5); w.Body.String() != png {
			t.Fatal("not served")
		}
	}
	if r.asked.Load() != 1 {
		t.Errorf("OSM was asked %d times", r.asked.Load())
	}
	// a different tile is another request
	r.get(3, 2, 6)
	if r.asked.Load() != 2 {
		t.Errorf("%d", r.asked.Load())
	}
}

func TestManyAtOnceAreOneRequest(t *testing.T) {
	r := newRig(t)
	r.handler = func(w http.ResponseWriter, q *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Write([]byte(png))
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.get(4, 1, 1) }()
	}
	wg.Wait()
	if r.asked.Load() != 1 {
		t.Errorf("eight browsers, %d requests to OSM", r.asked.Load())
	}
}

func TestAnExpiredTileIsAskedAboutAndKeptOnNotModified(t *testing.T) {
	r := newRig(t)
	r.get(3, 1, 1)
	r.advance(2 * time.Hour) // past the hour it said
	r.handler = func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("not a conditional request: %v", q.Header)
		}
		w.Header().Set("Cache-Control", "max-age=7200")
		w.WriteHeader(http.StatusNotModified)
	}
	if w := r.get(3, 1, 1); w.Body.String() != png {
		t.Fatal("the held tile was not served")
	}
	if r.asked.Load() != 2 {
		t.Fatalf("%d", r.asked.Load())
	}
	r.advance(time.Hour) // inside the new two hours
	r.get(3, 1, 1)
	if r.asked.Load() != 2 {
		t.Error("asked again before the renewed lifetime ended")
	}
}

func TestATileWithNoLifetimeIsKeptSevenDays(t *testing.T) {
	r := newRig(t)
	r.handler = func(w http.ResponseWriter, q *http.Request) { w.Write([]byte(png)) }
	r.get(3, 1, 1)
	r.advance(6 * 24 * time.Hour)
	r.get(3, 1, 1)
	if r.asked.Load() != 1 {
		t.Error("refetched inside seven days")
	}
	r.advance(2 * 24 * time.Hour)
	r.get(3, 1, 1)
	if r.asked.Load() != 2 {
		t.Error("not refetched after seven days")
	}
}

func TestAnOldTileBeatsNoneWhenOSMFailsAndOSMIsLeftAloneAfterARefusal(t *testing.T) {
	r := newRig(t)
	r.get(3, 1, 1)
	r.advance(2 * time.Hour)
	r.handler = func(w http.ResponseWriter, q *http.Request) { http.Error(w, "slow down", http.StatusTooManyRequests) }
	if w := r.get(3, 1, 1); w.Code != 200 || w.Body.String() != png {
		t.Fatalf("the stale tile was not served: %d", w.Code)
	}
	asked := r.asked.Load()
	// a tile never held: refused, and not asked for while OSM said to wait
	if w := r.get(3, 2, 2); w.Code != http.StatusBadGateway {
		t.Errorf("%d", w.Code)
	}
	r.get(3, 3, 3)
	r.get(3, 1, 1)
	if r.asked.Load() != asked {
		t.Errorf("asked %d more times during the hold", r.asked.Load()-asked)
	}
	r.advance(11 * time.Minute)
	r.handler = func(w http.ResponseWriter, q *http.Request) { w.Write([]byte(png)) }
	if w := r.get(3, 2, 2); w.Code != 200 {
		t.Errorf("after the hold: %d", w.Code)
	}
}

func TestNoSuchTileAndNotATile(t *testing.T) {
	r := newRig(t)
	for _, c := range [][3]int{{19, 0, 0}, {-1, 0, 0}, {3, 8, 0}, {3, 0, 8}, {3, -1, 0}} {
		if w := r.get(c[0], c[1], c[2]); w.Code != 404 {
			t.Errorf("%v: %d", c, w.Code)
		}
	}
	if r.asked.Load() != 0 {
		t.Error("OSM was asked about a tile that cannot exist")
	}
	r.handler = func(w http.ResponseWriter, q *http.Request) { http.NotFound(w, q) }
	if w := r.get(3, 1, 1); w.Code != 404 {
		t.Errorf("OSM's 404: %d", w.Code)
	}
	r.handler = func(w http.ResponseWriter, q *http.Request) { w.Write([]byte("<html>captcha</html>")) }
	if w := r.get(3, 1, 2); w.Code != http.StatusBadGateway {
		t.Errorf("not a PNG: %d", w.Code)
	}
	if _, err := os.Stat(r.c.Dir + "/3/1/2.png"); err == nil {
		t.Error("kept something that is not a tile")
	}
}

func TestTheOldestTilesGoWhenTheDirectoryIsFull(t *testing.T) {
	r := newRig(t)
	r.c.MaxBytes = int64(len(png)) * 3
	for y := 0; y < 3; y++ {
		r.get(5, 1, y)
		time.Sleep(20 * time.Millisecond) // the files' times order the deletions
	}
	r.get(5, 1, 3) // the fourth passes the limit
	has := func(y int) bool {
		_, err := os.Stat(r.c.Dir + "/5/1/" + string(rune('0'+y)) + ".png")
		return err == nil
	}
	if has(0) || !has(3) {
		t.Errorf("oldest %v, newest %v", has(0), has(3))
	}
	if _, err := os.Stat(r.c.Dir + "/5/1/0.meta"); err == nil {
		t.Error("the evicted tile's meta file is still there")
	}
}
