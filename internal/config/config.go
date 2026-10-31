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
	} `yaml:"adsb"`

	// AeroDataBox is the optional fallback for finding a flight by number when ADS-B has lost it, such as
	// over an ocean. Leave the key empty to turn it off.
	AeroDataBox struct {
		APIKey string        `yaml:"api_key"`
		Every  time.Duration `yaml:"every"` // least time between lookups per watch; default 30m
	} `yaml:"aerodatabox"`

	// Navdata holds the optional waypoint database (fix.dat.gz, nav.dat.gz); see scripts/fetch-navdata.sh.
	Navdata struct {
		Dir string `yaml:"dir"` // default data/navdata
	} `yaml:"navdata"`

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
