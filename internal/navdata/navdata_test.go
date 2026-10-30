package navdata

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixSample = `I
600 Version - data cycle 2013.10, build 20131334, metadata FixXP700.  Copyright (c) 2013, Robin A. Peel

 00.000000  000.000000 0000E
 04.000000  125.350000 PEDNO
 18.985000  115.845000 SABNO
 -4.965000  141.000000 KADAB
 40.000000  -100.000000 KADAB
 not a number here
 91.000000  010.000000 BADLAT
 10.000000  200.000000 BADLON
 12.5
99
`

const navSample = `
810 Version - data cycle 2013.10, build 20131334, metadata NavXP810.
2  36.848369 -002.379156      0   396  50    0.0 AMN ALMERIA NDB
3  -3.614919  128.186061      0 11260 130    0.0 AMN AMBON VOR-DME
12 -3.614919  128.186061      0 11260 130    0.0 AMN AMBON VOR-DME
3  -8.750583  115.163306      0 11320 130    0.0 BLI BALI VOR-DME
4  47.000000 -122.000000      0 11000 18 343.0 ISEA 16R KSEA ILS-cat-I
99
`

func write(t *testing.T, dir, name, content string, gz bool) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if gz {
		zw := gzip.NewWriter(f)
		zw.Write([]byte(content))
		zw.Close()
		return
	}
	f.WriteString(content)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fix.dat.gz", fixSample, true)
	write(t, dir, "nav.dat", navSample, false) // plain files work as well as gzipped ones
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Cycle != "2013.10" {
		t.Errorf("cycle = %q", d.Cycle)
	}
	if got := d.Lookup("sabno"); len(got) != 1 || got[0].Lat != 18.985 || got[0].Lon != 115.845 || got[0].Kind != "fix" {
		t.Errorf("SABNO = %+v", got)
	}
	// Two different places with one name are both kept, for the caller to choose between.
	if got := d.Lookup("KADAB"); len(got) != 2 {
		t.Errorf("KADAB = %+v", got)
	}
	// Navaids: the VOR and its DME at the same spot count once; the NDB elsewhere is separate; ILS lines are ignored.
	if got := d.Lookup("AMN"); len(got) != 2 {
		t.Errorf("AMN = %+v", got)
	}
	if got := d.Lookup("BLI"); len(got) != 1 || got[0].Kind != "vor" {
		t.Errorf("BLI = %+v", got)
	}
	if d.Lookup("ISEA") != nil {
		t.Error("an ILS was loaded as a navaid")
	}
	// Garbage lines and out-of-range coordinates are skipped, not fatal.
	for _, bad := range []string{"BADLAT", "BADLON", "number"} {
		if d.Lookup(bad) != nil {
			t.Errorf("%s was loaded", bad)
		}
	}
	if d.Lookup("NOSUCH") != nil || (*DB)(nil).Lookup("X") != nil {
		t.Error("unknown name or nil DB should find nothing")
	}
}

func TestLoadMissingAndLimits(t *testing.T) {
	if _, err := Load(t.TempDir()); !os.IsNotExist(err) {
		t.Errorf("empty dir: %v", err)
	}
	// A corrupt gzip is an error, not a hang or a panic.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "fix.dat.gz"), []byte("this is not gzip"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Error("corrupt gzip accepted")
	}
	// An absurdly long line does not blow the scanner up.
	dir = t.TempDir()
	write(t, dir, "fix.dat", "1 2 OK\n"+strings.Repeat("x", 2<<20)+"\n3 4 LATER\n", false)
	d, err := Load(dir)
	if err == nil && d.Lookup("LATER") != nil {
		t.Log("scanner survived the long line")
	}
}

func TestNear(t *testing.T) {
	d := New()
	d.Add("SABNO", Point{Lat: 18.985, Lon: 115.845, Kind: "fix"})
	d.Add("PUPIT", Point{Lat: -12.0, Lon: 115.150556, Kind: "fix"})
	d.Add("DLINE", Point{Lat: 5.0, Lon: 179.999, Kind: "fix"})
	d.Add("NEARBY", Point{Lat: 18.99, Lon: 115.85, Kind: "fix"}) // about 0.4 nm from SABNO
	if n, ok := d.Near(18.985, 115.845, 0.5); !ok || n != "SABNO" {
		t.Errorf("exact position = %q %v", n, ok)
	}
	if n, ok := d.Near(-12.0, 115.15, 0.5); !ok || n != "PUPIT" { // the ADS-C coordinate is rounded relative to the database
		t.Errorf("rounded position = %q %v", n, ok)
	}
	if n, _ := d.Near(18.9885, 115.8475, 0.5); n != "NEARBY" && n != "SABNO" {
		t.Errorf("between two: %q", n)
	}
	if n, ok := d.Near(18.985, 115.9, 0.5); ok {
		t.Errorf("3 nm away found %q", n)
	}
	// the cell wraps at the date line
	if n, ok := d.Near(5.0, -179.999, 0.5); !ok || n != "DLINE" {
		t.Errorf("across the date line = %q %v", n, ok)
	}
	if _, ok := (*DB)(nil).Near(0, 0, 1); ok {
		t.Error("nil DB found something")
	}
}
