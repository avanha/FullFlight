//go:build live

package airframes

import (
	"context"
	"testing"
)

func TestLiveLookup(t *testing.T) {
	a, err := NewClient().ByTail(context.Background(), "B-LRM")
	if err != nil || a == nil || a.ICAO != "780AB8" {
		t.Fatalf("%+v %v", a, err)
	}
	t.Logf("%+v", a)
}
