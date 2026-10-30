# FullFlight

Self-hosted tracker that follows specific aircraft by combining live ADS-B
positions (adsb.lol, adsb.fi) with ACARS/ADS-C/CPDLC datalink messages
(airframes.io), decoded with libacars. See `HANDOFF.md` for the design and
`docs/airframes-api.md` for API findings.

## Build and run

    brew install cmake pkgconf jansson   # macOS; libxml2/zlib come with the SDK
    make libacars                        # clones and builds libacars into third_party/
    make test
    make build                           # -> bin/fullflight
    cp config.example.yaml config.yaml   # edit; then:
    bin/fullflight -config config.yaml

## Watchlist

Type into the web UI (or `POST /api/watches {"input": "...", "until": "<RFC3339>"}`):

- `UA869 2026-10-31`: waits for the callsign (UAL869) to appear, then follows
  that aircraft by hex. Window: 12 h before the date (UTC) through 60 h.
- `B-LRM` or `780AB8`: tracks now until `until`, default 36 h. A tail is
  resolved to its hex via airframes.io, so it can be watched before it flies.

A flight watch ends when its aircraft lands and stays on the ground longer than
`poll.stopover_grace` (default 3 h), or starts flying under a different
callsign. A shorter stop is treated as a stopover and watching continues.
ADS-B carries no destination, so this is inferred from behaviour.

Every hex/tail pairing ever seen (from ADS-B or airframes.io) is kept in the
`airframe` table forever, independent of watches; see `GET /api/airframes`.

## Status

Done: watchlist, ADS-B poller with source fallback, SQLite track log, message
decoder (libacars + hand parsers), web UI with map. Not yet: airframes.io
message fetcher and its UI, flight segmentation, systemd/reverse-proxy docs.
