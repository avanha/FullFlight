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
heard by several stations is stored once (`rx_count`). ADS-C, CPDLC, REP081 and FMS (`POS…`)
positions are placed in time from their embedded timestamps, drawn on the map
in orange, and marked suspect only when they are impossible (over 700 kt) against every one of
the two nearest fixes on each side, so a coherent run of fixes is never
condemned by a conflicting one and the result does not depend on fetch order.
FMS position reports are placed at their receipt time, which is the time
airframes.io stored them (its `timestamp` equals its `createdAt`; it is not read
from the receiving station's clock). Their embedded waypoint time agrees with
that for some stations and is hours off for others, for reasons not yet
understood, so FMS positions can be misplaced in time. Those whose waypoint time
is more than 10 minutes from arrival are marked ⏱ (time unverified), drawn faint
and dotted, and never offered as the aircraft's last position. The newest ADS-C predicted route is drawn dashed.

`GET /api/messages/{hex}?hours=48&limit=300` returns them, oldest first. Airline-
specific engine/performance data (label H1 and others) has no public format; it
is kept raw and hidden in the timeline behind a "show N undecoded" link.
Uplink vs downlink is taken from airframes.io when given, else from which end of
the link is the aircraft (`fromHex`/`toHex`), else tried both ways.

After improving the decoder, run `bin/fullflight -config config.yaml -redecode` to
re-decode every stored message from its raw text (idempotent).

## Destination from the aircraft

Cathay A350 REP081 reports name the flight's origin and destination (`VHHH YSSY`).
The newest one is shown on the watch (`VHHH → YSSY (REP081 12m ago)`) and in the plane's
hover, for tail and hex watches as well as flights. REP081 goes out about every 20
minutes in flight, so a report older than 90 minutes is shown greyed with "may be out
of date" (and left out of the hover), because the aircraft may have landed and moved on
to another sector; after 24 hours it is not shown. If you gave a destination
yourself and the aircraft reports a different one, the line says so. Other airlines'
aircraft send no such report, so this appears only where the data exists.

## Current flight

A tail or hex watch follows an aircraft from flight to flight, so it shows the flight it is
operating now (`flight CPA113 · CX113 (from messages 4m ago)`), from whichever is newer: the
callsign on its latest ADS-B position, or the flight number in its latest message (the
10-character prefix, or an AFN logon), with the other form worked out from the airline
table. It goes grey after 90 minutes ("may be out of date") and disappears after 24 hours,
like the reported destination. A message whose text names a different aircraft is ignored.
The destination is deliberately not guessed from D-ATIS requests: crews also request
them for alternates and ETOPS enroute alternates (on Cathay flights with a known
destination, it was the first airport requested only 4 times in 16 and the last only 6).

On flight and hex watches the tail has a 📋 button that copies it, so an aircraft first
followed by callsign can be re-added by registration.

## Web page

Choosing a watch centres the map (zoom 7) on that aircraft's newest trustworthy
fix, from ADS-B or a datalink message, whichever is later. Each position source has its own track
and colour (ADS-B blue, ADS-C orange, CPDLC purple, REP081 teal, FMS green) with a
legend to switch them on and off. A track is broken, not bridged, across a long
silence (ADS-B 20 min, ADS-C 90, CPDLC 150, REP081 90, FMS 60) or an impossible
jump (over 700 kt), and positions flagged impossible are left out. Click a message that
has a position (orange bar on its left) to ring it on the map and show its popup
(only one popup is ever open); the map pans only if it is out of view. Hover a message's time for its age.
The aircraft is drawn as a plane turned to its track; hovering it shows altitude,
climb/descent trend, ground speed and track. `make uitest` (needs node) drives the
page script against a fake DOM.

## Status

Done: watchlist, ADS-B poller with source fallback, SQLite track log, permanent
hex/tail table, message fetcher and decoder, web UI with map, message timeline
and UTC clock. Not yet: CPDLC reports that name a fix instead of coordinates, flight segmentation
by tail/flight/date, MIAM continuation blocks (shown as "not decoded"),
ADS-C predicted-vs-actual ETA error, SSE live updates, systemd/reverse-proxy docs.
