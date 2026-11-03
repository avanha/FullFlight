// Package config loads the single YAML config file.
package config

import (
	"fmt"
	"net"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`  // default 127.0.0.1:8080
	DBPath string `yaml:"db_path"` // default data/fullflight.db

	Auth struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"auth"`

	ADSB struct {
		Sources []string `yaml:"sources"` // "adsb.lol", "adsb.fi", "airplanes.live" (needs its operators' permission); tried in order
		// Spread rotates which source is asked first so each carries a share of the load (default true). With
		// false the first is always asked first. airplanes.live is always last either way.
		Spread *bool `yaml:"spread"`
		// LolAPIKey is sent with every request to adsb.lol (as X-API-Key). Keys come from adsb.lol (feeders, or on request);
		// it answers without one, so leaving this empty is fine.
		LolAPIKey string `yaml:"lol_api_key"`
	} `yaml:"adsb"`

	// AeroDataBox is the optional fallback for finding a flight by number when ADS-B has lost it, such as
	// over an ocean. Leave the key empty to turn it off.
	AeroDataBox struct {
		APIKey string        `yaml:"api_key"`
		Every  time.Duration `yaml:"every"` // least time between lookups per watch; default 30m
	} `yaml:"aerodatabox"`

	// Navdata holds the optional waypoint database (fix.dat.gz, nav.dat.gz); fetch it with `fullflight -fetch-navdata`.
	Navdata struct {
		Dir string `yaml:"dir"` // default data/navdata
	} `yaml:"navdata"`

	// Backfill is the optional history of an aircraft's track from before we followed it, from the daily archives adsb.lol
	// publishes. Days are downloaded into CacheDir when a backfill is started from the menu; nothing is fetched unless Enabled.
	Backfill struct {
		Enabled     bool   `yaml:"enabled"`
		CacheDir    string `yaml:"cache_dir"`     // where the downloaded days are kept
		CacheMaxGiB int    `yaml:"cache_max_gib"` // oldest days are deleted beyond this; default 20
	} `yaml:"backfill"`

	// Weather is the optional record of the radar around the aircraft being followed, so the weather an aircraft flew
	// through can be seen again. Nothing is fetched unless Record is set.
	Weather struct {
		Record bool          `yaml:"record"`
		Dir    string        `yaml:"dir"`     // default data/weather
		MaxMiB int           `yaml:"max_mib"` // oldest frames are deleted beyond this; default 1024
		Every  time.Duration `yaml:"every"`   // between visits to RainViewer; default 75m, and under 2h, how far back it lists
	} `yaml:"weather"`

	Poll struct {
		Waiting  time.Duration `yaml:"waiting"`  // unmatched flight entries; default 3m
		Tracking time.Duration `yaml:"tracking"` // followed aircraft; default 45s
		// A flight watch ends once its aircraft has sat on the ground this long
		// (shorter stops count as stopovers); default 3h.
		StopoverGrace time.Duration `yaml:"stopover_grace"`
	} `yaml:"poll"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.DBPath == "" {
		c.DBPath = "data/fullflight.db"
	}
	if c.Navdata.Dir == "" {
		c.Navdata.Dir = "data/navdata"
	}
	if c.ADSB.Spread == nil {
		on := true
		c.ADSB.Spread = &on
	}
	if c.Backfill.CacheMaxGiB == 0 {
		c.Backfill.CacheMaxGiB = 20
	}
	if c.Weather.Dir == "" {
		c.Weather.Dir = "data/weather"
	}
	if c.Weather.MaxMiB == 0 {
		c.Weather.MaxMiB = 1024
	}
	if c.Weather.Every == 0 {
		c.Weather.Every = 75 * time.Minute
	}
	if len(c.ADSB.Sources) == 0 {
		c.ADSB.Sources = []string{"adsb.lol", "adsb.fi"}
	}
	return &c, c.validate()
}

// validate refuses to serve a non-loopback address without credentials.
func (c *Config) validate() error {
	if c.Auth.Password != "" && c.Auth.Username == "" {
		return fmt.Errorf("auth.username is required when auth.password is set")
	}
	if c.Backfill.Enabled && c.Backfill.CacheDir == "" {
		return fmt.Errorf("backfill.cache_dir is required when backfill.enabled is set")
	}
	if c.Backfill.CacheMaxGiB < 0 {
		return fmt.Errorf("backfill.cache_max_gib must not be negative")
	}
	if c.Weather.MaxMiB < 0 {
		return fmt.Errorf("weather.max_mib must not be negative")
	}
	if c.Weather.Every < 10*time.Minute || c.Weather.Every > 100*time.Minute {
		return fmt.Errorf("weather.every must be between 10m and 100m: RainViewer lists only the last two hours, so it has to be visited more often than that")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", c.Listen, err)
	}
	if c.Auth.Password == "" && !isLoopback(host) {
		return fmt.Errorf("listen %q is not loopback: set auth.username and auth.password", c.Listen)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
