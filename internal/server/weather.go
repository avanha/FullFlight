package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/weather"
)

type radarFrameJSON struct {
	ID    int64     `json:"id"` // the frame's time in Unix seconds, which names it
	Time  time.Time `json:"time"`
	Tiles int       `json:"tiles"`
}

// radarFrames lists the radar frames that were recorded, oldest first, so the page can pick the one nearest a time.
// Recording says whether the recorder is on now, which tells an empty list from a list that was never going to fill.
func (s *Server) radarFrames(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Recording bool             `json:"recording"`
		Zoom      int              `json:"zoom"`
		Frames    []radarFrameJSON `json:"frames"`
	}{Recording: s.WeatherRecording, Zoom: weather.RadarZoom, Frames: []radarFrameJSON{}}
	if s.WeatherDir != "" {
		frames, err := s.Store.WxFrames(weather.SourceRadar)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		for _, f := range frames {
			out.Frames = append(out.Frames, radarFrameJSON{f.Time.Unix(), f.Time, f.Tiles})
		}
	}
	writeJSON(w, 200, out)
}

// radarFrameTiles lists which tiles of a frame were recorded: the map layer asks for only those.
func (s *Server) radarFrameTiles(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("frame"), 10, 64)
	if err != nil || s.WeatherDir == "" {
		http.NotFound(w, r)
		return
	}
	tiles, err := s.Store.WxFrameTiles(weather.SourceRadar, time.Unix(id, 0).UTC())
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if len(tiles) == 0 {
		http.NotFound(w, r)
		return
	}
	out := make([][2]int, 0, len(tiles))
	for _, t := range tiles {
		if t.Z == weather.RadarZoom {
			out = append(out, [2]int{t.X, t.Y})
		}
	}
	writeJSON(w, 200, map[string]any{"z": weather.RadarZoom, "tiles": out})
}

// radarTile serves a recorded tile. A frame's tiles never change, so a browser may keep one for good.
func (s *Server) radarTile(w http.ResponseWriter, r *http.Request) {
	id, errF := strconv.ParseInt(r.PathValue("frame"), 10, 64)
	z, errZ := strconv.Atoi(r.PathValue("z"))
	x, errX := strconv.Atoi(r.PathValue("x"))
	y, errY := strconv.Atoi(strings.TrimSuffix(r.PathValue("y"), ".png"))
	if s.WeatherDir == "" || errF != nil || errZ != nil || errX != nil || errY != nil || z != weather.RadarZoom || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		http.NotFound(w, r)
		return
	}
	frame := time.Unix(id, 0).UTC()
	if !s.Store.HaveWxTile(weather.SourceRadar, frame, z, x, y) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(weather.TilePath(s.WeatherDir, frame, weather.Tile{Z: z, X: x, Y: y}))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", frame, f)
}
