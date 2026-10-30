// Command fullflight runs the watchlist poller and the web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/config"
	"github.com/avanha/fullflight/internal/datalink"
	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/poller"
	"github.com/avanha/fullflight/internal/server"
	"github.com/avanha/fullflight/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	redecode := flag.Bool("redecode", false, "re-decode all stored messages with the current decoder, then exit")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
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

	if *redecode {
		n, err := datalink.Redecode(st)
		if err != nil {
			log.Error("redecode", "err", err)
			os.Exit(1)
		}
		log.Info("redecoded", "changed", n)
		return
	}

	nav, err := navdata.Load(cfg.Navdata.Dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		log.Info("no navigation database; run scripts/fetch-navdata.sh to place more of the waypoints CPDLC reports name", "dir", cfg.Navdata.Dir)
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
			sources = append(sources, adsb.ADSBLol)
		case "adsb.fi":
			sources = append(sources, adsb.ADSBFi)
		default:
			log.Error("unknown adsb source", "name", name)
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &poller.Poller{Store: st, ADSB: adsb.NewClient(sources...), Log: log,
		Airframes: airframes.NewClient(), WaitingEvery: cfg.Poll.Waiting, TrackingEvery: cfg.Poll.Tracking,
		StopoverGrace: cfg.Poll.StopoverGrace}
	go p.Run(ctx)

	fetcher := &datalink.Fetcher{Store: st, Source: airframes.NewClient(), Log: log}
	go fetcher.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           (&server.Server{Store: st, Nav: nav, Username: cfg.Auth.Username, Password: cfg.Auth.Password}).Handler(),
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
