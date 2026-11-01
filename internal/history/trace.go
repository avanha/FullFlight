// Package history reads the daily ADS-B archives that adsb.lol publishes (github.com/adsblol/globe_history_YYYY) and
// turns an aircraft's trace in them into stored positions, for the time before we started following it.
package history

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/store"
)

// Source marks positions that came from the archive rather than from polling.
const Source = "adsb.lol-hist"

// A trace file is gzipped JSON from readsb: a base time, and one row per point,
//
//	[seconds after the base, lat, lon, altitude (feet or "ground"), ground speed, track, flags, vertical rate, details, source, ...]
//
// where details is an object on the rows that carry the callsign, or null.
type traceFile struct {
	Reg       string  `json:"r"`
	Timestamp float64 `json:"timestamp"`
	Trace     [][]any `json:"trace"`
}

// staleFlag marks a point whose position has not been updated for a while: it repeats an older one.
const staleFlag = 1

// ParseTrace reads one aircraft's trace file. The callsign is carried forward from the rows that name it.
func ParseTrace(r io.Reader) ([]store.Position, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	var f traceFile
	if err := json.NewDecoder(zr).Decode(&f); err != nil {
		return nil, err
	}
	reg := strings.ToUpper(f.Reg)
	var out []store.Position
	callsign := ""
	for _, row := range f.Trace {
		if len(row) < 8 {
			continue
		}
		if len(row) > 8 {
			if d, ok := row[8].(map[string]any); ok {
				if fl, ok := d["flight"].(string); ok && strings.TrimSpace(fl) != "" {
					callsign = strings.ToUpper(strings.TrimSpace(fl))
				}
			}
		}
		off, ok1 := row[0].(float64)
		lat, ok2 := row[1].(float64)
		lon, ok3 := row[2].(float64)
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		if flags, _ := row[6].(float64); int(flags)&staleFlag != 0 {
			continue
		}
		p := store.Position{
			T:   time.Unix(0, int64((f.Timestamp+off)*1e9)).UTC().Truncate(time.Second),
			Lat: lat, Lon: lon, Callsign: callsign, Reg: reg, Source: Source,
		}
		switch alt := row[3].(type) {
		case float64:
			p.AltFt = &alt
		case string:
			p.OnGround = alt == "ground"
		}
		p.GS, _ = number(row[4])
		p.Track, _ = number(row[5])
		p.VRate, _ = number(row[7])
		out = append(out, p)
	}
	if len(out) == 0 && len(f.Trace) > 0 {
		return nil, fmt.Errorf("trace has %d rows and none could be read", len(f.Trace))
	}
	return out, nil
}

func number(v any) (*float64, bool) {
	f, ok := v.(float64)
	if !ok {
		return nil, false
	}
	return &f, true
}
