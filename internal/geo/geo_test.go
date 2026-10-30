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
