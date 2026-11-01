// Package decoder turns raw ACARS application text into structured JSON using golibacars, a Go port of libacars.
package decoder

import (
	"encoding/json"
	"fmt"
	"strings"

	acars "github.com/avanha/golibacars"
)

type Direction int

const (
	Unknown  Direction = Direction(acars.Unknown)
	Uplink   Direction = Direction(acars.Uplink)
	Downlink Direction = Direction(acars.Downlink)
)

// Decoded is libacars' output for one message.
type Decoded struct {
	Tree map[string]any // the JSON tree
	Text string         // libacars' human-readable rendering
}

// DecodeApps decodes ACARS application text (already stripped of any
// message-number/flight-ID prefix) with the given label and direction.
// It returns libacars' JSON tree, or nil if libacars recognised nothing.
func DecodeApps(label, text string, dir Direction) (map[string]any, error) {
	d, err := DecodeAppsFull(label, text, dir)
	if d == nil {
		return nil, err
	}
	return d.Tree, err
}

// DecodeAppsFull is DecodeApps plus the text rendering. It returns nil, nil if
// libacars recognised nothing.
func DecodeAppsFull(label, text string, dir Direction) (*Decoded, error) {
	r := acars.Decode(label, text, acars.Dir(dir))
	if r == nil {
		return nil, nil
	}
	var out Decoded
	if err := json.Unmarshal([]byte(r.JSON()), &out.Tree); err != nil {
		return nil, fmt.Errorf("libacars: bad JSON: %w", err)
	}
	out.Text = strings.TrimSpace(r.Text())
	return &out, nil
}
