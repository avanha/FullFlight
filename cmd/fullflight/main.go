// Command fullflight runs the watchlist poller and the web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
	"github.com/avanha/fullflight/internal/aerodatabox"
	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/config"
	"github.com/avanha/fullflight/internal/datalink"
	"github.com/avanha/fullflight/internal/history"
	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/poller"
	"github.com/avanha/fullflight/internal/reqcount"
	"github.com/avanha/fullflight/internal/server"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/tilecache"
	"github.com/avanha/fullflight/internal/version"
	"github.com/avanha/fullflight/internal/weather"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	stats := flag.Bool("stats", false, "print how much data is stored, then exit")
	purgeDays := flag.Float64("purge-older-than", 0, "delete positions, messages and finished watches older than this many days, then exit; airframes with notes keep all their data. Only reports what it would do unless -confirm is given")
	confirm := flag.Bool("confirm", false, "with -purge-older-than: really delete")
	redecode := flag.Bool("redecode", false, "re-decode all stored messages with the current decoder, then exit")
	backfillDay := flag.String("backfill", "", "read the cached adsb.lol archive of this day (YYYY-MM-DD) and add the positions of every aircraft we hold anything for, then exit; stop the app first")
	fetchNav := flag.Bool("fetch-navdata", false, "download the optional waypoint database (FlightGear's, GPL, from 2013) into the navdata directory, then exit")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if *fetchNav {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		client := &http.Client{Timeout: 3 * time.Minute}
		if err := navdata.Fetch(ctx, client, navdata.DefaultBase, version.UserAgent(), cfg.Navdata.Dir, os.Stdout); err != nil {
			log.Error("fetching the navigation database", "err", err)
			os.Exit(1)
		}
		nav, err := navdata.Load(cfg.Navdata.Dir)
		if err != nil {
			log.Error("the downloaded navigation database is unusable", "err", err)
			os.Exit(1)
		}
		fmt.Printf("done: %s (cycle %s, %d fixes, %d navaids)\n", cfg.Navdata.Dir, nav.Cycle, nav.Fixes, nav.Navaid)
		return
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if *stats {
		u, err := st.Usage()
		if err != nil {
			log.Error("measuring the database", "err", err)
			os.Exit(1)
		}
		fmt.Print(u.Report())
		return
	}
	if *purgeDays != 0 {
		if *purgeDays < 1 {
			fmt.Fprintln(os.Stderr, "-purge-older-than takes a number of days, at least 1")
			os.Exit(2)
		}
		cutoff := time.Now().Add(-time.Duration(*purgeDays * 24 * float64(time.Hour)))
		before, _ := st.Usage()
		res, err := st.Purge(cutoff, !*confirm)
		if err != nil {
			log.Error("purge", "err", err)
			os.Exit(1)
		}
		fmt.Print(res.Report())
		if !*confirm {
			fmt.Println("Nothing was deleted. Run it again with -confirm to delete.")
			return
		}
		if err := st.Compact(); err != nil {
			fmt.Println("The space was not given back to the disk (is the app running? stop it and run this again):", err)
			return
		}
		after, _ := st.Usage()
		fmt.Printf("Database %s -> %s\n", store.Bytes(before.DBBytes), store.Bytes(after.DBBytes))
		return
	}
	if *redecode {
		stats, err := datalink.RedecodeStats(st)
		if err != nil {
			log.Error("redecode", "err", err, "messages", stats.Messages, "changed", stats.Changed, "after", stats.Total().Round(time.Millisecond))
			os.Exit(1)
		}
		log.Info("redecoded", "messages", stats.Messages, "changed", stats.Changed, "aircraft", stats.Aircraft,
			"load", stats.Load.Round(time.Millisecond), "decode", stats.Decode.Round(time.Millisecond), "settle", stats.Settle.Round(time.Millisecond),
			"total", stats.Total().Round(time.Millisecond), "per_message", stats.PerMessage().Round(time.Microsecond), "messages_per_second", int(stats.PerSecond()))
		return
	}

	if *backfillDay != "" {
		if !cfg.Backfill.Enabled {
			fmt.Fprintln(os.Stderr, "backfill is off: set backfill.enabled and backfill.cache_dir in the config")
			os.Exit(2)
		}
		hexes, err := st.TrackedHexes()
		if err != nil {
			log.Error("listing the aircraft", "err", err)
			os.Exit(1)
		}
		res, err := history.ImportDay(st, history.Cache{Dir: cfg.Backfill.CacheDir}, *backfillDay, hexes, time.Now())
		if err != nil {
			log.Error("backfill", "day", *backfillDay, "err", err)
			os.Exit(1)
		}
		fmt.Printf("%s: looked for %d aircraft (%d already done), %d had a trace, %d positions added\n",
			res.Day, res.Searched, res.Skipped, res.Found, res.Total())
		return
	}

	nav, err := navdata.Load(cfg.Navdata.Dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		log.Info("no navigation database; run fullflight -fetch-navdata to place more of the waypoints CPDLC reports name", "dir", cfg.Navdata.Dir)
		nav = nil
	case err != nil:
		log.Warn("navigation database unusable; continuing without it", "err", err)
		nav = nil
	default:
		log.Info("navigation database loaded", "cycle", nav.Cycle, "fixes", nav.Fixes, "navaids", nav.Navaid)
	}

	var sources []adsb.Source
	for _, name := range cfg.ADSB.Sources {
		switch name {
		case "adsb.lol":
			lol := adsb.ADSBLol
			if lol.APIKey = strings.TrimSpace(cfg.ADSB.LolAPIKey); lol.APIKey != "" {
				log.Info("adsb.lol requests will carry the API key from the configuration")
			}
			sources = append(sources, lol)
		case "adsb.fi":
			sources = append(sources, adsb.ADSBFi)
		case "airplanes.live":
			sources = append(sources, adsb.AirplanesLive)
		default:
			log.Error("unknown adsb source", "name", name)
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// every request to another service is counted by host, for the stats page
	count := func(host string, status int, failed bool) { st.CountRequest(time.Now(), host, status, failed) }
	afClient := airframes.NewClient()
	reqcount.Wrap(afClient.HTTP, count)
	adsbClient := adsb.NewClient(sources...)
	reqcount.Wrap(adsbClient.HTTP, count)
	adsbClient.Spread = *cfg.ADSB.Spread
	adsbClient.OnPause = func(source string, status int, until time.Time) {
		log.Warn("adsb source refused us; leaving it alone", "source", source, "http", status, "until", until.UTC().Format("15:04:05Z"))
	}
	p := &poller.Poller{Store: st, ADSB: adsbClient, Log: log,
		Airframes: afClient, Search: afClient, WaitingEvery: cfg.Poll.Waiting, TrackingEvery: cfg.Poll.Tracking,
		StopoverGrace: cfg.Poll.StopoverGrace}
	if cfg.AeroDataBox.APIKey != "" {
		adb := aerodatabox.NewClient(cfg.AeroDataBox.APIKey)
		reqcount.Wrap(adb.HTTP, count)
		p.Flights, p.ProviderEvery = adb, cfg.AeroDataBox.Every
		log.Info("flight-number lookup enabled", "provider", "aerodatabox")
	}
	go p.Run(ctx)

	fetcher := &datalink.Fetcher{Store: st, Source: afClient, Search: afClient, Log: log}
	go fetcher.Run(ctx)

	api := &server.Server{Store: st, Nav: nav, Username: cfg.Auth.Username, Password: cfg.Auth.Password, StationList: afClient}
	if cfg.Backfill.Enabled {
		if fi, err := os.Stat(cfg.Backfill.CacheDir); err != nil || !fi.IsDir() {
			log.Warn("backfill cache directory is not there; backfill will fail until it is", "dir", cfg.Backfill.CacheDir, "err", err)
		}
		api.Backfill = &history.Manager{Store: st, Cache: history.Cache{Dir: cfg.Backfill.CacheDir}, Log: log,
			MaxBytes: int64(cfg.Backfill.CacheMaxGiB) << 30,
			Fetch:    &history.Fetcher{UserAgent: version.UserAgent(), Log: log, Client: countedClient(&http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute}}, count)}}
		go func() { <-ctx.Done(); api.Backfill.Cancel() }()
		log.Info("backfill enabled", "cache", cfg.Backfill.CacheDir, "limit_gib", cfg.Backfill.CacheMaxGiB)
	}
	api.WeatherDir, api.WeatherRecording = cfg.Weather.Dir, cfg.Weather.Record
	if cfg.MapTiles.Cache {
		api.Tiles = &tilecache.Cache{Dir: cfg.MapTiles.Dir, MaxBytes: int64(cfg.MapTiles.MaxMiB) << 20, UserAgent: version.UserAgent(), Referer: version.Repo, Log: log,
			HTTP: countedClient(&http.Client{Timeout: 15 * time.Second}, count)}
		log.Info("serving OpenStreetMap tiles from a cache", "dir", cfg.MapTiles.Dir, "limit_mib", cfg.MapTiles.MaxMiB)
	}
	if cfg.Weather.Record {
		rec := &weather.Recorder{Store: st, Dir: cfg.Weather.Dir, UserAgent: version.UserAgent(), Step: cfg.Weather.Step, MaxBytes: int64(cfg.Weather.MaxMiB) << 20, Log: log,
			HTTP: countedClient(&http.Client{Timeout: 30 * time.Second}, count)}
		go rec.Run(ctx, cfg.Weather.Every)
		log.Info("recording radar along followed flights", "dir", cfg.Weather.Dir, "visit_every", cfg.Weather.Every, "frame_every", cfg.Weather.Step, "limit_mib", cfg.Weather.MaxMiB)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", cfg.Listen, "db", cfg.DBPath, "sources", cfg.ADSB.Sources)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// countedClient makes c count its requests with record and returns it.
func countedClient(c *http.Client, record func(host string, status int, failed bool)) *http.Client {
	reqcount.Wrap(c, record)
	return c
}
