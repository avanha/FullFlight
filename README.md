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

Add an optional destination, IATA or ICAO: `UA869 2026-10-31 SFO`.

- **With a destination**, the watch ends when the aircraft is on the ground
  within 10 nm of that airport (or was last seen low, within 15 nm, and has been
  silent for 10 min). Landing anywhere else is a stopover or diversion and
  watching continues, even under a new callsign; it only ends after 4 x
  `poll.stopover_grace` (12 h) on the ground.
- **Without one**, ADS-B gives no destination, so it is inferred from
  behaviour: the watch ends when the aircraft stays on the ground longer than
  `poll.stopover_grace` (default 3 h) or starts flying under a different
  callsign. A shorter stop is treated as a stopover.

Airport data: OurAirports (public domain), large and medium airports plus any
with an IATA code, embedded from `internal/airports/airports.csv`.

Every hex/tail pairing ever seen (from ADS-B or airframes.io) is kept in the
`airframe` table forever, independent of watches; see `GET /api/airframes`.

## Status

Done: watchlist, ADS-B poller with source fallback, SQLite track log, message
decoder (libacars + hand parsers), web UI with map. Not yet: airframes.io
message fetcher and its UI, flight segmentation, systemd/reverse-proxy docs.
