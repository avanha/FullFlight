// Package navdata looks up named waypoints and navaids. It reads the X-Plane format files
// FlightGear ships (fix.dat and nav.dat), which are free but from 2013, so a name may be missing
// or have been reused since; callers must check a position against what they know of the flight.
//
// The files are untrusted input: reading is size-limited and malformed lines are skipped.
package navdata

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MaxFileBytes bounds how much is read from one (decompressed) file; the real ones are ~4 MB.
const MaxFileBytes = 64 << 20

type Point struct {
	Lat, Lon float64
	Kind     string // "fix", "vor", "ndb" or "dme"
}

type DB struct {
	byName map[string][]Point
	Fixes  int    // fix.dat entries loaded
	Navaid int    // nav.dat entries loaded
	Cycle  string // AIRAC-style cycle of the data, e.g. "2013.10", if the header gave one
}

func New() *DB { return &DB{byName: map[string][]Point{}} }

// Add records a point under a name. Two points under one name within 0.01 degree (a VOR and its
// DME) are kept once.
func (d *DB) Add(name string, p Point) {
	name = strings.ToUpper(name)
	for _, q := range d.byName[name] {
		if abs(q.Lat-p.Lat) < 0.01 && abs(q.Lon-p.Lon) < 0.01 {
			return
		}
	}
	d.byName[name] = append(d.byName[name], p)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// Lookup returns every point with this name (fixes and navaids can share one).
func (d *DB) Lookup(name string) []Point {
	if d == nil {
		return nil
	}
	return d.byName[strings.ToUpper(name)]
}

func (d *DB) Len() int { return len(d.byName) }

var cycleRe = regexp.MustCompile(`data cycle (\d{4}\.\d{2})`)

// Load reads fix.dat(.gz) and nav.dat(.gz) from dir. It fails only if neither is present.
func Load(dir string) (*DB, error) {
	d := New()
	found := false
	for _, f := range []struct {
		base  string
		parse func(*DB, io.Reader) error
	}{{"fix.dat", parseFix}, {"nav.dat", parseNav}} {
		r, err := open(filepath.Join(dir, f.base))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		err = f.parse(d, io.LimitReader(r, MaxFileBytes))
		r.Close()
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, os.ErrNotExist
	}
	return d, nil
}

type readCloser struct {
	io.Reader
	closers []io.Closer
}

func (r readCloser) Close() error {
	for _, c := range r.closers {
		c.Close()
	}
	return nil
}

// open opens base.gz or base, whichever exists.
func open(base string) (io.ReadCloser, error) {
	if f, err := os.Open(base + ".gz"); err == nil {
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return readCloser{zr, []io.Closer{zr, f}}, nil
	}
	return os.Open(base)
}

func scanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	return sc
}

func latLon(a, b string) (lat, lon float64, ok bool) {
	lat, e1 := strconv.ParseFloat(a, 64)
	lon, e2 := strconv.ParseFloat(b, 64)
	return lat, lon, e1 == nil && e2 == nil && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// parseFix reads lines of "lat lon NAME ...".
func parseFix(d *DB, r io.Reader) error {
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if m := cycleRe.FindStringSubmatch(line); m != nil && d.Cycle == "" {
			d.Cycle = m[1]
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		lat, lon, ok := latLon(f[0], f[1])
		if !ok {
			continue
		}
		d.Add(f[2], Point{lat, lon, "fix"})
		d.Fixes++
	}
	return sc.Err()
}

// parseNav reads NDB (2), VOR (3) and DME (12, 13) lines: "type lat lon elev freq range var IDENT name...".
func parseNav(d *DB, r io.Reader) error {
	kinds := map[string]string{"2": "ndb", "3": "vor", "12": "dme", "13": "dme"}
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if m := cycleRe.FindStringSubmatch(line); m != nil && d.Cycle == "" {
			d.Cycle = m[1]
		}
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		kind, ok := kinds[f[0]]
		if !ok {
			continue
		}
		lat, lon, ok := latLon(f[1], f[2])
		if !ok {
			continue
		}
		d.Add(f[7], Point{lat, lon, kind})
		d.Navaid++
	}
	return sc.Err()
}
