package decoder

import "testing"

func TestDecodeAppsUnknownLabel(t *testing.T) {
	got, err := DecodeApps("ZZ", "hello", Downlink)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}
