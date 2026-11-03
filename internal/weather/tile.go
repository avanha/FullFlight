// Package weather records the radar tiles around the aircraft being followed, so the weather an aircraft flew through can be
// drawn again after the live feeds have moved on.
package weather

import (
	"math"
	"sort"
	"time"
)

// Tile is a slippy-map tile: x to the east from the date line, y to the south from the north pole.
type Tile struct{ Z, X, Y int }

// Point is where an aircraft was, and when.
type Point struct {
	T        time.Time
	Lat, Lon float64
}

// maxLat is where the web mercator map stops.
const maxLat = 85.0511

// frac is a place in tile units at zoom z: the whole part is the tile.
func frac(lat, lon float64, z int) (fx, fy float64) {
	n := float64(int(1) << z)
	lat = math.Max(-maxLat, math.Min(maxLat, lat))
	r := lat * math.Pi / 180
	fx = (lon + 180) / 360 * n
	fy = (1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n
	return
}

func tileOf(fx, fy float64, z int) Tile {
	n := 1 << z
	x := int(math.Floor(fx)) % n
	if x < 0 {
		x += n
	}
	y := int(math.Floor(fy))
	return Tile{z, x, max(0, min(n-1, y))}
}

// TileAt is the tile that holds the place.
func TileAt(lat, lon float64, z int) Tile {
	fx, fy := frac(lat, lon, z)
	return tileOf(fx, fy, z)
}

// Around lists the tile and its neighbours out to ring tiles away: ring 1 is a 3 by 3 block. The map wraps at the date line
// and stops at the poles.
func Around(t Tile, ring int) []Tile {
	n := 1 << t.Z
	var out []Tile
	seen := map[Tile]bool{}
	for dy := -ring; dy <= ring; dy++ {
		y := t.Y + dy
		if y < 0 || y >= n {
			continue
		}
		for dx := -ring; dx <= ring; dx++ {
			a := Tile{t.Z, ((t.X+dx)%n + n) % n, y}
			if !seen[a] { // a map narrower than the block meets itself
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// PositionAt is where the aircraft was at t, if the fixes (oldest first) bracket it within maxGap of each other; it never
// extrapolates.
func PositionAt(fixes []Point, t time.Time, maxGap time.Duration) (Point, bool) {
	i := sort.Search(len(fixes), func(i int) bool { return !fixes[i].T.Before(t) })
	switch {
	case i < len(fixes) && fixes[i].T.Equal(t):
		return fixes[i], true
	case i == 0 || i == len(fixes):
		return Point{}, false
	}
	a, b := fixes[i-1], fixes[i]
	if b.T.Sub(a.T) > maxGap {
		return Point{}, false
	}
	f := float64(t.Sub(a.T)) / float64(b.T.Sub(a.T))
	dlon := b.Lon - a.Lon
	if dlon > 180 {
		dlon -= 360
	} else if dlon < -180 {
		dlon += 360
	}
	lon := a.Lon + dlon*f
	if lon > 180 {
		lon -= 360
	} else if lon < -180 {
		lon += 360
	}
	return Point{T: t, Lat: a.Lat + (b.Lat-a.Lat)*f, Lon: lon}, true
}

// TilesAlong lists the tiles within ring of the aircraft's path from from to to, at zoom z, ordered north to south then west
// to east. The path runs through the fixes in the window, and from where the fixes either side put the aircraft at its start
// and end, when they are no more than maxGap apart. No fixes, and nothing to interpolate from, gives no tiles.
func TilesAlong(fixes []Point, from, to time.Time, z, ring int, maxGap time.Duration) []Tile {
	var path []Point
	if p, ok := PositionAt(fixes, from, maxGap); ok {
		path = append(path, p)
	}
	for _, f := range fixes {
		if f.T.After(from) && f.T.Before(to) {
			path = append(path, f)
		}
	}
	if p, ok := PositionAt(fixes, to, maxGap); ok {
		path = append(path, p)
	}
	n := float64(int(1) << z)
	seen := map[Tile]bool{}
	add := func(fx, fy float64) {
		for _, t := range Around(tileOf(fx, fy, z), ring) {
			seen[t] = true
		}
	}
	for i, p := range path {
		fx, fy := frac(p.Lat, p.Lon, z)
		if i == 0 {
			add(fx, fy)
			continue
		}
		qx, qy := frac(path[i-1].Lat, path[i-1].Lon, z)
		dx := fx - qx
		if dx > n/2 {
			dx -= n
		} else if dx < -n/2 {
			dx += n
		}
		dy := fy - qy
		// a sample every half tile along the leg, so a leg across a corner does not skip the tile it cuts
		steps := int(math.Ceil(math.Max(math.Abs(dx), math.Abs(dy)) / 0.5))
		for k := 1; k <= steps; k++ {
			f := float64(k) / float64(steps)
			x := qx + dx*f
			if x < 0 {
				x += n
			} else if x >= n {
				x -= n
			}
			add(x, qy+dy*f)
		}
	}
	out := make([]Tile, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Y != out[j].Y {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out
}
