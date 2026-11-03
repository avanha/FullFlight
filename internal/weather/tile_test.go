package weather

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func TestTileAt(t *testing.T) {
	// the standard slippy-map examples: Greenwich at zoom 0 is the one tile; Sydney at zoom 7 is column 117 (151.21 east is 117.75 of 128) and row 76 (by the mercator formula)
	if got := TileAt(51.48, 0, 0); got != (Tile{0, 0, 0}) {
		t.Errorf("zoom 0: %v", got)
	}
	if got := TileAt(-33.86, 151.21, 7); got != (Tile{7, 117, 76}) {
		t.Errorf("Sydney: %v", got)
	}
	// the date line is the western edge, and a longitude past it wraps; the poles clamp to the top and bottom rows
	if got := TileAt(0, -180, 3); got.X != 0 {
		t.Errorf("-180: %v", got)
	}
	if got := TileAt(0, 180, 3); got.X != 0 {
		t.Errorf("+180 wraps to the first column: %v", got)
	}
	if a, b := TileAt(90, 0, 3), TileAt(-90, 0, 3); a.Y != 0 || b.Y != 7 {
		t.Errorf("poles: %v %v", a, b)
	}
}

func TestAround(t *testing.T) {
	if got := Around(Tile{7, 50, 50}, 2); len(got) != 25 {
		t.Errorf("ring 2 is 5 by 5: %d", len(got))
	}
	if got := Around(Tile{7, 0, 0}, 1); len(got) != 6 {
		t.Errorf("a corner at the pole keeps two rows, and wraps west to the last column: %d", len(got))
	}
	found := false
	for _, x := range Around(Tile{7, 0, 40}, 1) {
		found = found || x.X == 127
	}
	if !found {
		t.Error("west of column 0 is column 127")
	}
	if got := Around(Tile{1, 0, 0}, 3); len(got) != 4 {
		t.Errorf("a block wider than the map is the map once, not repeated: %d", len(got))
	}
}

func TestPositionAt(t *testing.T) {
	fixes := []Point{{t0, 10, 170}, {t0.Add(20 * time.Minute), 10, -170}}
	p, ok := PositionAt(fixes, t0.Add(10*time.Minute), time.Hour)
	if !ok || p.Lat != 10 || p.Lon != -180 && p.Lon != 180 {
		t.Errorf("halfway across the date line: %+v %v", p, ok)
	}
	if _, ok := PositionAt(fixes, t0.Add(10*time.Minute), 10*time.Minute); ok {
		t.Error("fixes further apart than the gap are not interpolated")
	}
	if _, ok := PositionAt(fixes, t0.Add(-time.Minute), time.Hour); ok {
		t.Error("never extrapolated before the first fix")
	}
	if _, ok := PositionAt(fixes, t0.Add(21*time.Minute), time.Hour); ok {
		t.Error("never extrapolated after the last")
	}
	if p, ok := PositionAt(fixes, t0, time.Minute); !ok || p.Lon != 170 {
		t.Error("an exact time is the fix itself, whatever the gap")
	}
}

func TestTilesAlongCoversTheWholeLeg(t *testing.T) {
	// a leg of about 8 zoom-7 tiles west to east: with ring 0 every tile it crosses, with none skipped
	fixes := []Point{{t0, 0, 100}, {t0.Add(10 * time.Minute), 0, 100 + 8*360.0/128}}
	got := TilesAlong(fixes, t0, t0.Add(10*time.Minute), 7, 0, time.Hour)
	if len(got) != 9 {
		t.Fatalf("%d tiles: %v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].X != got[i-1].X+1 {
			t.Errorf("a gap in the tiles: %v", got)
		}
	}
	// the ring widens it
	if r1 := TilesAlong(fixes, t0, t0.Add(10*time.Minute), 7, 1, time.Hour); len(r1) != 11*3 {
		t.Errorf("ring 1 is the leg's tiles plus one either side, three rows: %d", len(r1))
	}
}

func TestTilesAlongWindowAndGaps(t *testing.T) {
	fixes := []Point{{t0, 0, 100}, {t0.Add(2 * time.Hour), 0, 110}}
	// the window falls in a two-hour silence: with a one-hour limit nothing is guessed
	if got := TilesAlong(fixes, t0.Add(50*time.Minute), t0.Add(70*time.Minute), 7, 1, time.Hour); len(got) != 0 {
		t.Errorf("no guess across a long silence: %v", got)
	}
	// a window with no fix inside it takes the path from the fixes either side
	fixes = []Point{{t0, 0, 100}, {t0.Add(40 * time.Minute), 0, 104}}
	got := TilesAlong(fixes, t0.Add(10*time.Minute), t0.Add(30*time.Minute), 7, 0, time.Hour)
	if len(got) < 2 {
		t.Errorf("the interpolated path crosses tiles: %v", got)
	}
	// one fix in the window is a single block
	if got := TilesAlong([]Point{{t0, 40, -100}}, t0.Add(-5*time.Minute), t0.Add(5*time.Minute), 7, 1, time.Hour); len(got) != 9 {
		t.Errorf("a single fix: %d", len(got))
	}
	// across the date line the path goes the short way
	fixes = []Point{{t0, 0, 179.5}, {t0.Add(10 * time.Minute), 0, -179.5}}
	got = TilesAlong(fixes, t0, t0.Add(10*time.Minute), 7, 0, time.Hour)
	if len(got) != 2 || got[0].X != 0 || got[1].X != 127 {
		t.Errorf("the date line: %v", got)
	}
}
