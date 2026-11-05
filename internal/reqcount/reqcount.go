// Package reqcount counts the requests we make to other people's services, by host, so the stats page can show how much of a free
// service's allowance has gone.
package reqcount

import "net/http"

// Transport passes requests on to Base (http.DefaultTransport when nil) and reports each one's host and outcome to Record.
type Transport struct {
	Base   http.RoundTripper
	Record func(host string, status int, failed bool)
}

// RoundTrip reports status 0 and failed when the request got no answer at all.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if t.Record != nil {
		if err != nil {
			t.Record(req.URL.Host, 0, true)
		} else {
			t.Record(req.URL.Host, resp.StatusCode, resp.StatusCode >= 400)
		}
	}
	return resp, err
}

// Wrap makes c count its requests. A client that already counts is left alone, so wrapping twice does not count twice.
func Wrap(c *http.Client, record func(host string, status int, failed bool)) {
	if _, ok := c.Transport.(*Transport); ok {
		return
	}
	c.Transport = &Transport{Base: c.Transport, Record: record}
}
