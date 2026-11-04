package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestADSBLolAPIKey(t *testing.T) {
	if c, err := load(t, "{}"); err != nil || c.ADSB.LolAPIKey != "" {
		t.Fatalf("default: %q %v", c.ADSB.LolAPIKey, err)
	}
	if c, err := load(t, "adsb: {lol_api_key: abc123}"); err != nil || c.ADSB.LolAPIKey != "abc123" {
		t.Errorf("key not read: %q %v", c.ADSB.LolAPIKey, err)
	}
}

func TestWeatherConfig(t *testing.T) {
	c, err := load(t, "{}")
	if err != nil || c.Weather.Record || c.Weather.Dir != "data/weather" || c.Weather.MaxMiB != 1024 || c.Weather.Every != 30*time.Minute || c.Weather.Step != 20*time.Minute {
		t.Fatalf("%+v %v", c.Weather, err)
	}
	c, err = load(t, "weather: {record: true, dir: /w, max_mib: 50, every: 45m, step: 30m}")
	if err != nil || !c.Weather.Record || c.Weather.Dir != "/w" || c.Weather.MaxMiB != 50 || c.Weather.Every != 45*time.Minute || c.Weather.Step != 30*time.Minute {
		t.Errorf("%+v %v", c.Weather, err)
	}
	for _, bad := range []string{"weather: {every: 2h}", "weather: {every: 5m}", "weather: {max_mib: -1}", "weather: {step: 15m}", "weather: {step: 5m}", "weather: {step: 2h}"} {
		if _, err := load(t, bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestMapTilesConfig(t *testing.T) {
	c, err := load(t, "{}")
	if err != nil || c.MapTiles.Cache || c.MapTiles.Dir != "data/tiles" || c.MapTiles.MaxMiB != 500 {
		t.Fatalf("%+v %v", c.MapTiles, err)
	}
	c, err = load(t, "map_tiles: {cache: true, dir: /t, max_mib: 64}")
	if err != nil || !c.MapTiles.Cache || c.MapTiles.Dir != "/t" || c.MapTiles.MaxMiB != 64 {
		t.Errorf("%+v %v", c.MapTiles, err)
	}
	if _, err := load(t, "map_tiles: {max_mib: -1}"); err == nil {
		t.Error("a negative limit")
	}
}

func TestBackfillConfig(t *testing.T) {
	c, err := load(t, "{}")
	if err != nil || c.Backfill.Enabled || c.Backfill.CacheMaxGiB != 20 {
		t.Fatalf("%+v %v", c.Backfill, err)
	}
	if _, err := load(t, "backfill: {enabled: true}"); err == nil {
		t.Error("backfill on without a cache directory")
	}
	c, err = load(t, "backfill: {enabled: true, cache_dir: /Volumes/External/FullFlightCache, cache_max_gib: 100}")
	if err != nil || c.Backfill.CacheDir != "/Volumes/External/FullFlightCache" || c.Backfill.CacheMaxGiB != 100 {
		t.Errorf("%+v %v", c.Backfill, err)
	}
	if _, err := load(t, "backfill: {cache_max_gib: -1}"); err == nil {
		t.Error("a negative limit")
	}
}
