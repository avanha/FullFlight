package weather

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	gopng "image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

// png is the start of a PNG file: all the recorder checks of a tile is that it is one.
const png = "\x89PNG\r\n\x1a\n-pixels-"

// rig is a RainViewer that serves an index of frames and tiles, counting what it was asked.
type rig struct {
	t      *testing.T
	srv    *httptest.Server
	st     *store.Store
	rec    *Recorder
	mu     sync.Mutex
	asked  []string
	status map[string]int // path prefix -> a status to answer with instead
	frames []int64
}

func newRig(t *testing.T, frames ...time.Time) *rig {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, status: map[string]int{}}
	for _, f := range frames {
		r.frames = append(r.frames, f.Unix())
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.asked = append(r.asked, q.URL.Path)
		r.mu.Unlock()
		if !strings.Contains(q.Header.Get("User-Agent"), "FullFlight") {
			t.Errorf("no User-Agent naming us: %q", q.Header.Get("User-Agent"))
		}
		for prefix, code := range r.status {
			if strings.HasPrefix(q.URL.Path, prefix) {
				http.Error(w, "no", code)
				return
			}
		}
		if q.URL.Path == "/index.json" {
			var past []map[string]any
			for _, f := range r.frames {
				past = append(past, map[string]any{"time": f, "path": fmt.Sprintf("/v2/radar/f%d", f)})
			}
			json.NewEncoder(w).Encode(map[string]any{"host": "https://tilecache.rainviewer.com", "radar": map[string]any{"past": past}})
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte(png))
	}))
	t.Cleanup(r.srv.Close)
	r.rec = &Recorder{Store: st, Dir: t.TempDir(), UserAgent: "FullFlight/test", IndexURL: r.srv.URL + "/index.json", Host: r.srv.URL,
		Pause: time.Millisecond, Now: func() time.Time { return t0.Add(time.Hour) }}
	return r
}

func (r *rig) follow(hex string, pts ...Point) {
	r.t.Helper()
	if _, err := r.st.AddWatch(watchlist.Entry{Kind: watchlist.KindHex, Ident: hex, Hex: hex, Status: watchlist.StatusTracking, Start: t0, End: t0.Add(24 * time.Hour)}); err != nil {
		r.t.Fatal(err)
	}
	for _, p := range pts {
		if _, err := r.st.AddPosition(store.Position{Hex: hex, T: p.T, Lat: p.Lat, Lon: p.Lon, Source: "test"}); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) tilesAsked() (n int) {
	for _, p := range r.asked {
		if strings.Contains(p, "/256/") {
			n++
		}
	}
	return
}

// Frames are every ten minutes; the recorder takes the ones on a multiple of its step.
func tenMinute(n int) (out []time.Time) {
	for i := 0; i < n; i++ {
		out = append(out, t0.Add(time.Duration(i)*10*time.Minute))
	}
	return
}

func TestRecordsTilesAroundTheAircraftForEveryOtherFrame(t *testing.T) {
	r := newRig(t, tenMinute(7)...) // 12:00 to 13:00
	r.rec.Ring = 1
	r.follow("780AB8", Point{t0, 0, 100}, Point{t0.Add(time.Hour), 0, 100.01}) // sits over one tile all hour
	res, err := r.rec.RecordOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// 12:00, 12:20, 12:40 and 13:00 are multiples of twenty minutes
	if res.Frames != 4 || res.Fetched != 4*9 || res.Had != 0 {
		t.Fatalf("%+v", res)
	}
	if r.tilesAsked() != 36 {
		t.Errorf("asked for %d tiles", r.tilesAsked())
	}
	want := TileAt(0, 100, RadarZoom)
	if !r.st.HaveWxTile(SourceRadar, t0.Add(20*time.Minute), want.Z, want.X, want.Y) {
		t.Error("the tile under the aircraft is not in the index")
	}
	if r.st.HaveWxTile(SourceRadar, t0.Add(10*time.Minute), want.Z, want.X, want.Y) {
		t.Error("an in-between frame was recorded")
	}
	b, err := os.ReadFile(r.rec.TilePath(t0.Add(20*time.Minute), want))
	if err != nil || string(b) != png {
		t.Errorf("the file: %q %v", b, err)
	}
	if n, _ := r.st.WxBytes(); n != int64(36*len(png)) {
		t.Errorf("bytes held: %d", n)
	}
	// the tile URL carries the scheme RainViewer actually serves, and the size and options the map layer uses
	if !strings.Contains(strings.Join(r.asked, " "), fmt.Sprintf("/v2/radar/f%d/256/7/%d/%d/2/1_1.png", t0.Unix(), want.X, want.Y)) {
		t.Errorf("asked: %v", r.asked[:3])
	}
}

func TestASecondVisitFetchesOnlyWhatIsNew(t *testing.T) {
	r := newRig(t, tenMinute(3)...) // 12:00 to 12:20
	r.follow("780AB8", Point{t0, 0, 100}, Point{t0.Add(time.Hour), 0, 100.01})
	r.rec.Ring = 1
	if _, err := r.rec.RecordOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	asked := r.tilesAsked()
	res, err := r.rec.RecordOnce(t.Context())
	if err != nil || res.Fetched != 0 || res.Had != 18 || r.tilesAsked() != asked {
		t.Errorf("%+v %v, tiles asked %d then %d", res, err, asked, r.tilesAsked())
	}
}

func TestNothingIsFetchedWhereNoAircraftWas(t *testing.T) {
	r := newRig(t, tenMinute(7)...)
	// no watches at all, then one that is on the ground, then one whose last fix is hours before the frames
	if res, err := r.rec.RecordOnce(t.Context()); err != nil || res.Fetched != 0 || r.tilesAsked() != 0 {
		t.Fatalf("%+v %v %d", res, err, r.tilesAsked())
	}
	r.follow("780AB8", Point{t0.Add(-9 * time.Hour), 0, 100})
	if res, err := r.rec.RecordOnce(t.Context()); err != nil || res.Fetched != 0 || r.tilesAsked() != 0 {
		t.Errorf("an old fix draws no tiles: %+v %v %d", res, err, r.tilesAsked())
	}
}

func TestAnAircraftOnTheGroundIsNotWhereTheWeatherMatters(t *testing.T) {
	r := newRig(t, tenMinute(3)...)
	r.follow("780AB8")
	if _, err := r.st.AddPosition(store.Position{Hex: "780AB8", T: t0, Lat: 0, Lon: 100, OnGround: true, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if res, err := r.rec.RecordOnce(t.Context()); err != nil || res.Fetched != 0 {
		t.Errorf("%+v %v", res, err)
	}
}

func TestATooManyRequestsEndsTheVisitAndIsRemembered(t *testing.T) {
	r := newRig(t, tenMinute(3)...)
	r.follow("780AB8", Point{t0, 0, 100}, Point{t0.Add(time.Hour), 0, 100.01})
	r.status["/v2/radar/"] = http.StatusTooManyRequests
	_, err := r.rec.RecordOnce(t.Context())
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("%v", err)
	}
	if r.tilesAsked() != 1 {
		t.Errorf("went on asking after a 429: %d", r.tilesAsked())
	}
	asked := len(r.asked)
	if _, err := r.rec.RecordOnce(t.Context()); err == nil || len(r.asked) != asked {
		t.Errorf("asked again while held: %v, %d then %d requests", err, asked, len(r.asked))
	}
	// the hold ends
	r.status = map[string]int{}
	r.rec.Now = func() time.Time { return t0.Add(3 * time.Hour) }
	if res, err := r.rec.RecordOnce(t.Context()); err != nil || res.Fetched == 0 {
		t.Errorf("after the hold: %+v %v", res, err)
	}
}

func TestAMissingTileIsRecordedWithNoFileAndNotAskedForAgain(t *testing.T) {
	r := newRig(t, tenMinute(3)...)
	r.follow("780AB8", Point{t0, 0, 100}, Point{t0.Add(time.Hour), 0, 100.01})
	r.rec.Ring = 1
	r.status["/v2/radar/"] = http.StatusNotFound
	res, err := r.rec.RecordOnce(t.Context())
	if err != nil || res.Fetched != 0 || res.Missing == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if n, _ := r.st.WxBytes(); n != 0 {
		t.Errorf("%d bytes recorded for tiles that were not there", n)
	}
	frames, _ := r.st.WxFrames(SourceRadar)
	tiles, _ := r.st.WxFrameTiles(SourceRadar, frames[0].Time)
	if len(tiles) != res.Missing/res.Frames || tiles[0].Status != store.WxNone {
		t.Errorf("a place with no radar is not held as such: %+v", tiles)
	}
	if _, err := os.Stat(r.rec.TilePath(frames[0].Time, Tile{Z: tiles[0].Z, X: tiles[0].X, Y: tiles[0].Y})); err == nil {
		t.Errorf("a file for a tile that was not there")
	}
	// the next visit does not ask again for what RainViewer said it does not have
	r.asked = nil
	res, err = r.rec.RecordOnce(t.Context())
	if err != nil || r.tilesAsked() != 0 || res.Had == 0 {
		t.Errorf("%+v %v, asked for %d tiles", res, err, r.tilesAsked())
	}
}

func TestATileWithNothingInItIsHeldAsClear(t *testing.T) {
	if !empty(transparentPNG(t, 0)) {
		t.Errorf("a wholly transparent picture is not clear")
	}
	if empty(transparentPNG(t, 255)) {
		t.Errorf("a picture with a pixel of rain is clear")
	}
	if empty([]byte(png)) {
		t.Errorf("a picture that will not decode must not be called clear")
	}
}

func TestATileThatIsNotAnImageIsRefused(t *testing.T) {
	r := newRig(t, tenMinute(1)...)
	r.follow("780AB8", Point{t0, 0, 100}, Point{t0.Add(time.Hour), 0, 100.01})
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/index.json" {
			fmt.Fprintf(w, `{"host":"x","radar":{"past":[{"time":%d,"path":"/v2/radar/f"}]}}`, t0.Unix())
			return
		}
		w.Write([]byte("<html>not a tile</html>"))
	})
	if _, err := r.rec.RecordOnce(t.Context()); err == nil || !strings.Contains(err.Error(), "not a PNG") {
		t.Errorf("%v", err)
	}
	if n, _ := r.st.WxBytes(); n != 0 {
		t.Error("something was kept")
	}
}

func TestOldestFramesAreDeletedBeyondTheLimit(t *testing.T) {
	r := newRig(t, tenMinute(7)...)
	r.follow("780AB8", Point{t0, 0, 100}, Point{t0.Add(time.Hour), 0, 100.01})
	r.rec.Ring = 1
	r.rec.MaxBytes = int64(2 * 9 * len(png)) // room for two frames' nine tiles
	res, err := r.rec.RecordOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 36 || res.Evicted != 2 {
		t.Fatalf("%+v", res)
	}
	if _, f, _ := r.st.OldestWxFrame(); !f.Equal(t0.Add(40 * time.Minute)) {
		t.Errorf("the oldest left is %v", f)
	}
	if _, err := os.Stat(r.rec.TilePath(t0, TileAt(0, 100, RadarZoom))); !os.IsNotExist(err) {
		t.Error("the file of an evicted frame is still there")
	}
	if _, err := os.Stat(r.rec.TilePath(t0.Add(time.Hour), TileAt(0, 100, RadarZoom))); err != nil {
		t.Errorf("the newest frame's file: %v", err)
	}
}

func TestTheIndexMustNameRainViewer(t *testing.T) {
	good := Index{Host: "https://tilecache.rainviewer.com", Frames: []Frame{{t0, "/v2/radar/05284dfd06cd"}}}
	if err := CheckHost(good); err != nil {
		t.Errorf("the real one: %v", err)
	}
	for _, bad := range []Index{
		{Host: "http://tilecache.rainviewer.com", Frames: good.Frames},
		{Host: "https://rainviewer.com.evil.example", Frames: good.Frames},
		{Host: "https://evil.example/rainviewer.com", Frames: good.Frames},
		{Host: "https://tilecache.rainviewer.com", Frames: []Frame{{t0, "/v2/radar/../../x"}}},
		{Host: "https://tilecache.rainviewer.com", Frames: []Frame{{t0, "/other/path"}}},
		{Host: "https://tilecache.rainviewer.com", Frames: []Frame{{t0, "/v2/radar/a?b=c"}}},
	} {
		if CheckHost(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestEveryKeepsTheFramesOnTheStep(t *testing.T) {
	ix, err := ParseIndex(strings.NewReader(`{"version":"2.0","host":"https://tilecache.rainviewer.com","radar":{"past":[
		{"time":1793736600,"path":"/v2/radar/a"},{"time":1793737200,"path":"/v2/radar/b"},{"time":1793737800,"path":"/v2/radar/c"}],"nowcast":[]}}`))
	if err != nil || len(ix.Frames) != 3 || ix.Host != "https://tilecache.rainviewer.com" {
		t.Fatalf("%+v %v", ix, err)
	}
	// the real index of 2026-11-03 had frames at :10, :20, :30 (a multiple of twenty minutes is :20 and :40)
	got := ix.Every(20 * time.Minute)
	if len(got) != 1 || got[0].Path != "/v2/radar/b" {
		t.Errorf("%v", got)
	}
	if len(ix.Every(0)) != 3 {
		t.Error("no step keeps every frame")
	}
	if _, err := ParseIndex(strings.NewReader("<html>")); err == nil {
		t.Error("not JSON")
	}
}

// transparentPNG is a small picture, wholly transparent except for one pixel of the alpha given.
func transparentPNG(t *testing.T, alpha uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	img.SetNRGBA(1, 1, color.NRGBA{R: 0, G: 100, B: 255, A: alpha})
	var b bytes.Buffer
	if err := gopng.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
