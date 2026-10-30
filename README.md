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
- `CPA113` or `UA869` (no date): a callsign, or a flight number with a known
  airline code, is a flight watch that starts now and runs 36 h (or until
  `until`). `CPA113 SFO` adds a destination.
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

## Datalink messages

For every aircraft on an open watch (once its hex is known) the app pulls
messages from airframes.io: 7 days of history on first sight, then every 3
minutes with a 30 minute overlap to catch late uploads. Each is decoded by us
from the raw text (libacars plus hand parsers), never from airframes.io's own
fields. Those are kept as `af_*` hints, and a disagreement (tail, flight number,
or a tail that belongs to a different hex) shows as a warning badge. A message
heard by several stations is stored once (`rx_count`). ADS-C, CPDLC and REP081
positions are placed in time from their embedded timestamps, drawn on the map
in orange, and marked suspect when they imply more than 700 kt against the
nearest other fix. The newest ADS-C predicted route is drawn dashed.

`GET /api/messages/{hex}?hours=48&limit=300` returns them, oldest first.

After improving the decoder, run `bin/fullflight -config config.yaml -redecode` to
re-decode every stored message from its raw text (idempotent).

## Status

Done: watchlist, ADS-B poller with source fallback, SQLite track log, permanent
hex/tail table, message fetcher and decoder, web UI with map, message timeline
and UTC clock. Not yet: CPDLC reports that name a fix instead of coordinates, flight segmentation
by tail/flight/date, MIAM continuation blocks (shown as "not decoded"),
ADS-C predicted-vs-actual ETA error, SSE live updates, systemd/reverse-proxy docs.
