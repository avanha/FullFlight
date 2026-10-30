package geo

import (
	"math"
	"testing"
)

func TestDistNM(t *testing.T) {
	if d := DistNM(0, 0, 0, 0); d != 0 {
		t.Errorf("same point = %v", d)
	}
	// SFO to LAX is about 294 nm.
	if d := DistNM(37.6198, -122.3748, 33.9425, -118.4081); math.Abs(d-294) > 4 {
		t.Errorf("SFO-LAX = %v", d)
	}
}

func TestLongitudeWrap(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{0, 0}, {179.9, 179.9}, {180, -180}, {181, -179}, {-181, 179}, {540, -180}, {359, -1}, {-180, -180}} {
		if got := NormLon(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("NormLon(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ a, b, want float64 }{{10, 20, 10}, {20, 10, -10}, {179, -179, 2}, {-179, 179, -2}, {170, -170, 20}, {0, 90, 90}} {
		if got := DLon(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("DLon(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	// Distances were already fine across the date line.
	if d := DistNM(0, 179.5, 0, -179.5); math.Abs(d-60) > 0.1 {
		t.Errorf("a degree across the date line = %v nm", d)
	}
}
