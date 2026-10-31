package datalink

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/airlines"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
	"github.com/avanha/fullflight/internal/waypoints"
)

// Source is the subset of airframes.Client the fetcher needs.
type Source interface {
	Messages(ctx context.Context, hex string, since, until time.Time) ([]airframes.Message, error)
}

// Searcher finds messages by their text. Flight plans are sent over VHF ACARS with no hex address, so the
// by-address fetch never returns them; they are found by the flight number they carry.
type Searcher interface {
	SearchText(ctx context.Context, s string) ([]airframes.Message, error)
}

type Fetcher struct {
	Store  *store.Store
	Source Source
	Search Searcher // optional: flight plans, by callsign
	Log    *slog.Logger
	Now    func() time.Time

	Interval time.Duration // between fetches for one aircraft; default 3m
	Backfill time.Duration // how far back the first fetch for an aircraft reaches; default 7 days
	// Overlap makes each fetch start this far before the previous one ended,
	// to pick up messages uploaded late by stations with a backlog; default 30m.
	Overlap  time.Duration
	MaxPages int // per aircraft per cycle; default 100

	// PlanEvery is the least time between flight-plan searches for one aircraft and callsign; default 15m.
	PlanEvery time.Duration

	last        map[string]time.Time
	planAt      map[string]time.Time
	pausedUntil time.Time
}

func (f *Fetcher) defaults() {
	if f.Now == nil {
		f.Now = time.Now
	}
	if f.Log == nil {
		f.Log = slog.Default()
	}
	if f.Interval == 0 {
		f.Interval = 3 * time.Minute
	}
	if f.Backfill == 0 {
		f.Backfill = 7 * 24 * time.Hour
	}
	if f.Overlap == 0 {
		f.Overlap = 30 * time.Minute
	}
	if f.MaxPages == 0 {
		f.MaxPages = 100
	}
	if f.PlanEvery == 0 {
		f.PlanEvery = 15 * time.Minute
	}
	if f.last == nil {
		f.last = map[string]time.Time{}
		f.planAt = map[string]time.Time{}
	}
}

func (f *Fetcher) Run(ctx context.Context) {
	f.defaults()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		f.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Once fetches for every aircraft on the open watchlist that is due.
func (f *Fetcher) Once(ctx context.Context) {
	f.defaults()
	now := f.Now()
	if now.Before(f.pausedUntil) {
		return
	}
	entries, err := f.Store.ListWatches(true)
	if err != nil {
		f.Log.Error("list watches", "err", err)
		return
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Hex == "" || seen[e.Hex] || !e.Active(now) {
			continue
		}
		seen[e.Hex] = true
		if t, ok := f.last[e.Hex]; ok && now.Sub(t) < f.Interval {
			continue
		}
		f.last[e.Hex] = now
		err := f.fetchHex(ctx, e.Hex, now)
		if err == nil {
			err = f.fetchPlans(ctx, e.Hex, entries, now)
		}
		if err != nil {
			var rl *airframes.RateLimitError
			if errors.As(err, &rl) {
				f.pausedUntil = now.Add(rl.RetryAfter)
				f.Log.Warn("airframes.io rate limited; pausing", "for", rl.RetryAfter)
				return
			}
			f.Log.Warn("message fetch failed", "hex", e.Hex, "err", err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// fetchHex pulls everything since the last successful fetch (less Overlap),
// paging backwards from now. Failing part-way leaves fetched_to alone so the
// same range is retried; ingest is idempotent.
func (f *Fetcher) fetchHex(ctx context.Context, hex string, now time.Time) error {
	since := now.Add(-f.Backfill)
	if t, ok := f.Store.FetchedTo(hex); ok {
		since = t.Add(-f.Overlap)
	}
	until, added := now, 0
	for page := 0; page < f.MaxPages; page++ {
		batch, err := f.Source.Messages(ctx, hex, since, until)
		if err != nil {
			return err
		}
		oldest := until
		for _, m := range batch {
			if m.Timestamp.Before(oldest) {
				oldest = m.Timestamp
			}
			ok, err := Ingest(f.Store, hex, m)
			if err != nil {
				f.Log.Error("ingest", "id", m.ID, "err", err)
				continue
			}
			if ok {
				added++
			}
		}
		if len(batch) < airframes.PageSize || !oldest.Before(until) {
			break // last page, or no progress (a full page at one instant)
		}
		until = oldest
	}
	if added > 0 {
		f.Log.Info("messages stored", "hex", hex, "new", added)
		// Pages arrive newest first, so early verdicts were made with half the
		// neighbours missing; settle them now.
		// A station may deliver a backlog days late: what sits among its dated messages is as old as they are.
		if n, err := MarkLate(f.Store, hex, since.Add(-FixWindow)); err != nil {
			f.Log.Error("mark late messages", "hex", hex, "err", err)
		} else if n > 0 {
			f.Log.Info("late-delivered messages flagged", "hex", hex, "changed", n)
		}
		if n, err := Recheck(f.Store, hex, since.Add(-FixWindow)); err != nil {
			f.Log.Error("recheck positions", "hex", hex, "err", err)
		} else if n > 0 {
			f.Log.Info("position flags updated", "hex", hex, "changed", n)
		}
		// CPDLC and ADS-C reports can arrive in either order, so match their waypoints over a wide window.
		if n, err := waypoints.Learn(f.Store, hex, since.Add(-2*time.Hour)); err != nil {
			f.Log.Error("learn waypoints", "hex", hex, "err", err)
		} else if n > 0 {
			f.Log.Debug("waypoint matches", "hex", hex, "n", n)
		}
	}
	return f.Store.SetFetchedTo(hex, now)
}

// fetchPlans looks for what the by-address fetch cannot return: messages sent with no hex address, which
// airframes.io ties to the aircraft by its registration or flight number. It searches the text for the
// registration (performance reports and logons name the flight), then for the flight plan of each flight the
// aircraft is known to be on: the callsigns on its watches and in its recent messages. Only this aircraft's
// results are kept (the search is across all aircraft), stored like any message.
func (f *Fetcher) fetchPlans(ctx context.Context, hex string, entries []watchlist.Entry, now time.Time) error {
	if f.Search == nil {
		return nil
	}
	added := 0
	search := func(key, q string) error {
		if now.Sub(f.planAt[key]) < f.PlanEvery {
			return nil
		}
		f.planAt[key] = now
		found, err := f.Search.SearchText(ctx, q)
		if err != nil {
			return err
		}
		for _, m := range found {
			if !strings.EqualFold(string(m.Airframe.ICAO), hex) {
				continue
			}
			if ok, err := Ingest(f.Store, hex, m); err != nil {
				f.Log.Error("ingest", "id", m.ID, "err", err)
			} else if ok {
				added++
			}
		}
		return nil
	}
	tail, _ := f.Store.TailForHex(hex)
	recent, _ := f.Store.Messages(hex, now.Add(-24*time.Hour), now.Add(time.Minute), 200)
	for _, m := range recent {
		if tail == "" {
			tail = m.Tail // as the aircraft named itself, if the pairing is not in the airframe table yet
		}
	}
	if tail != "" {
		if err := search(hex+"/"+tail, tail); err != nil {
			return err
		}
	}
	calls := map[string]bool{}
	for _, e := range entries {
		if e.Hex != hex {
			continue
		}
		if e.Callsign != "" {
			calls[e.Callsign] = true
		}
		if e.Kind == watchlist.KindFlight {
			if icao, _ := airlines.Forms(e.Ident); icao != "" {
				calls[icao] = true
			}
		}
	}
	recent, _ = f.Store.Messages(hex, now.Add(-24*time.Hour), now.Add(time.Minute), 200) // now with what the tail search found
	for _, m := range recent {
		if m.Callsign != "" {
			calls[m.Callsign] = true
		}
		if icao, _ := airlines.Forms(m.Flight); icao != "" {
			calls[icao] = true
		}
	}
	for cs := range calls {
		if err := search(hex+"/"+cs, "FPN/FN"+cs); err != nil {
			return err
		}
	}
	if added > 0 {
		f.Log.Info("messages stored from text search", "hex", hex, "new", added)
	}
	return nil
}
