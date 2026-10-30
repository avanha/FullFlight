// Package poller walks the open watchlist, queries ADS-B, latches flight
// entries onto an aircraft once seen, and records the track.
package poller

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/adsb"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

// Source is the subset of adsb.Client the poller needs.
type Source interface {
	Hex(ctx context.Context, hex string) ([]adsb.Aircraft, error)
	Callsign(ctx context.Context, cs string) ([]adsb.Aircraft, error)
	Reg(ctx context.Context, reg string) ([]adsb.Aircraft, error)
}

type Poller struct {
	Store *store.Store
	ADSB  Source
	Log   *slog.Logger
	Now   func() time.Time

	WaitingEvery  time.Duration // flight entries not yet matched; default 3m
	TrackingEvery time.Duration // entries following an aircraft; default 45s
	MaxPosAge     time.Duration // ignore positions older than this; default 60s

	last map[int64]time.Time
}

func (p *Poller) defaults() {
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Log == nil {
		p.Log = slog.Default()
	}
	if p.WaitingEvery == 0 {
		p.WaitingEvery = 3 * time.Minute
	}
	if p.TrackingEvery == 0 {
		p.TrackingEvery = 45 * time.Second
	}
	if p.MaxPosAge == 0 {
		p.MaxPosAge = 60 * time.Second
	}
	if p.last == nil {
		p.last = map[int64]time.Time{}
	}
}

// Run ticks until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	p.defaults()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		p.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Once expires finished entries and polls every entry that is due.
func (p *Poller) Once(ctx context.Context) {
	p.defaults()
	now := p.Now()
	if n, err := p.Store.ExpireWatches(now); err != nil {
		p.Log.Error("expire", "err", err)
	} else if n > 0 {
		p.Log.Info("expired watches", "n", n)
	}
	entries, err := p.Store.ListWatches(true)
	if err != nil {
		p.Log.Error("list watches", "err", err)
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !e.Active(now) {
			continue
		}
		every := p.TrackingEvery
		if e.Status == watchlist.StatusWaiting {
			every = p.WaitingEvery
		}
		if t, ok := p.last[e.ID]; ok && now.Sub(t) < every {
			continue
		}
		p.last[e.ID] = now
		if err := p.poll(ctx, e, now); err != nil {
			p.Log.Warn("poll failed", "watch", e.Ident, "err", err)
		}
	}
}

func (p *Poller) poll(ctx context.Context, e watchlist.Entry, now time.Time) error {
	var ac []adsb.Aircraft
	var err error
	switch {
	case e.Hex != "":
		ac, err = p.ADSB.Hex(ctx, e.Hex)
	case e.Kind == watchlist.KindTail:
		ac, err = p.ADSB.Reg(ctx, e.Ident)
	case e.Kind == watchlist.KindFlight:
		ac, err = p.findFlight(ctx, e)
	}
	if err != nil || len(ac) == 0 {
		return err // empty is normal: not airborne, or out of coverage
	}
	a := ac[0]
	changed := false
	if e.Hex == "" {
		e.Hex, changed = a.Hex, true
	}
	if e.Tail == "" && a.Reg != "" {
		e.Tail, changed = strings.ToUpper(a.Reg), true
	}
	if cs := a.Callsign(); cs != "" && cs != e.Callsign {
		e.Callsign, changed = cs, true
	}
	if e.Status == watchlist.StatusWaiting {
		e.Status, changed = watchlist.StatusTracking, true
		p.Log.Info("flight matched", "watch", e.Ident, "hex", e.Hex, "tail", e.Tail, "callsign", e.Callsign)
	}
	if changed {
		if err := p.Store.UpdateWatch(e); err != nil {
			return err
		}
	}
	if !a.HasPosition(p.MaxPosAge) {
		return nil
	}
	pos := store.Position{
		Hex: a.Hex, T: now.Add(-seconds(a.SeenPos)), Lat: *a.Lat, Lon: *a.Lon,
		AltFt: a.AltBaro.Feet, OnGround: a.AltBaro.OnGround, GS: a.GS, Track: a.Track,
		VRate: a.BaroRate, Callsign: a.Callsign(), Reg: strings.ToUpper(a.Reg), Source: a.Source,
	}
	_, err = p.Store.AddPosition(pos)
	return err
}

func seconds(f *float64) time.Duration {
	if f == nil {
		return 0
	}
	return time.Duration(*f * float64(time.Second))
}

// findFlight looks for the flight's callsign variants and returns the first hit.
func (p *Poller) findFlight(ctx context.Context, e watchlist.Entry) ([]adsb.Aircraft, error) {
	cands, err := CallsignCandidates(e.Ident)
	if err != nil {
		return nil, err
	}
	for _, cs := range cands {
		ac, err := p.ADSB.Callsign(ctx, cs)
		if err != nil {
			return nil, err
		}
		if len(ac) > 0 {
			return ac, nil
		}
	}
	return nil, nil
}
