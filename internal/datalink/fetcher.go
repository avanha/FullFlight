package datalink

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/store"
)

// Source is the subset of airframes.Client the fetcher needs.
type Source interface {
	Messages(ctx context.Context, hex string, since, until time.Time) ([]airframes.Message, error)
}

type Fetcher struct {
	Store  *store.Store
	Source Source
	Log    *slog.Logger
	Now    func() time.Time

	Interval time.Duration // between fetches for one aircraft; default 3m
	Backfill time.Duration // how far back the first fetch for an aircraft reaches; default 7 days
	// Overlap makes each fetch start this far before the previous one ended,
	// to pick up messages uploaded late by stations with a backlog; default 30m.
	Overlap  time.Duration
	MaxPages int // per aircraft per cycle; default 100

	last        map[string]time.Time
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
	if f.last == nil {
		f.last = map[string]time.Time{}
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
		if err := f.fetchHex(ctx, e.Hex, now); err != nil {
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
		if n, err := Recheck(f.Store, hex, since.Add(-FixWindow)); err != nil {
			f.Log.Error("recheck positions", "hex", hex, "err", err)
		} else if n > 0 {
			f.Log.Info("position flags updated", "hex", hex, "changed", n)
		}
	}
	return f.Store.SetFetchedTo(hex, now)
}
