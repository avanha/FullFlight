package config

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, y string) (*Config, error) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(y), 0o600)
	return Load(p)
}

func TestDefaults(t *testing.T) {
	c, err := load(t, "{}")
	if err != nil || c.Listen != "127.0.0.1:8080" || len(c.ADSB.Sources) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestPublicListenNeedsAuth(t *testing.T) {
	if _, err := load(t, "listen: 0.0.0.0:8080"); err == nil {
		t.Error("expected error for open non-loopback listener")
	}
	if _, err := load(t, "listen: 0.0.0.0:8080\nauth: {username: a, password: b}"); err != nil {
		t.Error(err)
	}
}

func TestDurations(t *testing.T) {
	c, err := load(t, "poll: {waiting: 5m, tracking: 30s}")
	if err != nil || c.Poll.Waiting.Minutes() != 5 || c.Poll.Tracking.Seconds() != 30 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSpreadDefaultsOn(t *testing.T) {
	c, err := load(t, "{}")
	if err != nil || c.ADSB.Spread == nil || !*c.ADSB.Spread {
		t.Fatalf("default: %v %v", c.ADSB.Spread, err)
	}
	if c, err = load(t, "adsb: {sources: [adsb.lol], spread: false}"); err != nil || *c.ADSB.Spread {
		t.Errorf("spread: false not honoured: %v %v", c.ADSB.Spread, err)
	}
}
