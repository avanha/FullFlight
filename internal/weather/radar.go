package weather

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// SourceRadar names RainViewer's global radar composite in the index of recorded tiles.
const SourceRadar = "radar"

// RadarZoom is the finest zoom RainViewer serves.
const RadarZoom = 7

// Frame is one radar picture. Its tiles never change, so a frame's path is also its name.
type Frame struct {
	Time time.Time
	Path string
}

// Index is RainViewer's list of the radar frames it has: the last two hours, ten minutes apart.
type Index struct {
	Host   string
	Frames []Frame // oldest first
}

// ParseIndex reads weather-maps.json.
func ParseIndex(r io.Reader) (Index, error) {
	var j struct {
		Host  string `json:"host"`
		Radar struct {
			Past []struct {
				Time int64  `json:"time"`
				Path string `json:"path"`
			} `json:"past"`
		} `json:"radar"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&j); err != nil {
		return Index{}, err
	}
	ix := Index{Host: j.Host}
	for _, f := range j.Radar.Past {
		ix.Frames = append(ix.Frames, Frame{time.Unix(f.Time, 0).UTC(), f.Path})
	}
	return ix, nil
}

// Every keeps the frames whose time is a multiple of step, so that a longer interval than RainViewer's own picks the same
// frames whichever burst finds them.
func (ix Index) Every(step time.Duration) []Frame {
	var out []Frame
	for _, f := range ix.Frames {
		if step <= 0 || f.Time.Unix()%int64(step/time.Second) == 0 {
			out = append(out, f)
		}
	}
	return out
}

// TileURL is where a frame's tile is. host is RainViewer's, from the index, unless the caller has checked it. The colour
// scheme is the one RainViewer serves whatever is asked for now (Universal Blue, 2), smoothed, with snow in its own colours:
// the same picture the map's radar layer shows.
func TileURL(host string, f Frame, t Tile) string {
	return fmt.Sprintf("%s%s/256/%d/%d/%d/2/1_1.png", host, f.Path, t.Z, t.X, t.Y)
}

// CheckHost refuses an index that sends us anywhere but a RainViewer host over https, or names a path that is not a radar
// frame: the tiles it names are fetched without further question.
func CheckHost(ix Index) error {
	u, err := url.Parse(ix.Host)
	if err != nil || u.Scheme != "https" || u.Path != "" && u.Path != "/" || !(u.Hostname() == "rainviewer.com" || strings.HasSuffix(u.Hostname(), ".rainviewer.com")) {
		return fmt.Errorf("the radar index names a host that is not RainViewer's: %q", ix.Host)
	}
	for _, f := range ix.Frames {
		if !strings.HasPrefix(f.Path, "/v2/radar/") || strings.Contains(f.Path, "..") || strings.ContainsAny(f.Path, "?#\\ ") {
			return fmt.Errorf("the radar index names a frame path that is not a radar frame: %q", f.Path)
		}
	}
	return nil
}
