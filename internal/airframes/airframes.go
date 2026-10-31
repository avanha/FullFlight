// Package airframes looks up hex <-> registration pairings on airframes.io.
package airframes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/version"
)

type Airframe struct {
	ID   int    `json:"id"`
	Tail string `json:"tail"`
	ICAO string `json:"icao"` // the 24-bit hex address
	Type string `json:"icaoType"`
}

type Client struct {
	BaseURL   string // default https://api.airframes.io/v1
	HTTP      *http.Client
	UserAgent string
}

func NewClient() *Client {
	return &Client{BaseURL: "https://api.airframes.io/v1", HTTP: &http.Client{Timeout: 15 * time.Second},
		UserAgent: version.UserAgent()}
}

// ByTail returns nil, nil if airframes.io does not know the registration.
func (c *Client) ByTail(ctx context.Context, tail string) (*Airframe, error) {
	return c.get(ctx, "/airframes/tail/"+url.PathEscape(strings.ToUpper(tail)))
}

// ByHex returns nil, nil if airframes.io does not know the address.
func (c *Client) ByHex(ctx context.Context, hex string) (*Airframe, error) {
	return c.get(ctx, "/airframes/icao/"+url.PathEscape(strings.ToUpper(hex)))
}

func (c *Client) get(ctx context.Context, path string) (*Airframe, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("airframes.io: HTTP %d", resp.StatusCode)
	}
	var a Airframe
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // airframes.io answers 200 with an empty body for an unknown registration
		}
		return nil, fmt.Errorf("airframes.io: bad JSON: %w", err)
	}
	a.ICAO, a.Tail = strings.ToUpper(a.ICAO), strings.ToUpper(a.Tail)
	if a.ICAO == "" {
		return nil, nil
	}
	return &a, nil
}
