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
- `CPA113`, `TZP1`, `CKK624` (no date): three letters then digits is a callsign, whatever the
  airline, since no registration looks like that (they are hyphenated, or start N, JA, HL...). It is a
  flight watch that starts now and runs 36 h (or until `until`); `CPA113 SFO` adds a destination.
  `UA869` (no date) reads the same way for airlines whose two-letter code we know.
- When the shape cannot tell, say so with a prefix: `flight:CK624` (a two-letter flight number for an
  airline we have no code for; the ICAO callsign always works), `tail:CKK624`, `hex:ABC123`. Six hex
  digits (`AEE123`) are an ICAO address unless we know the airline. The page says how it read each
  entry, and every watch carries a chip (callsign, registration, hex, flight).
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

## ADS-B sources

The configured sources are asked in order, but an empty answer is not final: sources hear
different parts of the sky, so the next one is asked, and the results are merged (fresher
position wins; a missing callsign, registration or type is filled in from the other). A
complete answer (a position under 20 s old and a callsign) stops the search, so the second
source costs nothing while the first has the aircraft. A source that errors is skipped while
another answers.

## Flights

An aircraft's history is divided into the flights it flew (`GET /api/flights/{hex}`), so a plane with
a week of data does not show as a tangle. A flight is a run of sightings of one flight number
(callsigns on ADS-B and flight numbers in message prefixes, in either form, `CPA113` = `CX113`),
split wherever the number is not seen for six hours, so the same number on two days is two flights.
Stray one-off numbers are folded away. Positions attach to the flight they fall in or within an hour
of. Positions that name no flight but cluster are shown as an *unidentified* flight rather than lost,
and pieces of one flight that report the same REP081 route are joined (a flight whose number was only
logged for its last hours is still one flight).

The **Flights** selector under the watch list chooses what the map and message list show: the **most
recent flight** (the default), **all flights**, or any one. In the all-flights view each flight is
drawn on its own (a line never joins two flights), older ones fainter and flagged with their flight
number and date, and the message list has a heading above each flight's messages. Clicking a heading or
a flag shows just that flight and fits the map to it. The plane and the projection appear only for the
flight in progress. The choice of most recent / all is remembered; a particular flight is not.

## Waypoints named in CPDLC reports

A CPDLC position report names the fix just passed, the next fix (with its ETA) and the one after,
but not where they are. Each name gets coordinates from the most trustworthy source that has them:

1. **Learned from the aircraft's own ADS-C** (exact): an ADS-C report lists its next waypoint with
   coordinates and an ETA; a CPDLC fix whose time matches that ETA to within 90 s is that waypoint.
2. **The navigation database** (exact), if it fits the flight: not behind the aircraft, no nearer than
   the next fix, within 60 nm of our own estimate. `scripts/fetch-navdata.sh` downloads FlightGear's
   X-Plane-format `fix.dat.gz`/`nav.dat.gz` into `data/navdata/`. It is GPL data from **2013**, so it
   misses newer waypoints (about half of the names seen on Cathay's Pacific routes) and some names have
   been reused (KADAB is 44 nm behind B-LRD in it); it is kept out of git and the app works without it.
3. **An estimate** (dashed): a passed fix interpolated along the aircraft's own track at the reported
   time; the next fix projected from the report's position, speed and track by its ETA. The fix after
   next has no time, so without 1 or 2 it is not placed.

Solid diamonds are exact, dashed ones estimates; hover for how each was placed. Clicking one selects
the report that named it. Position-report tooltips also show wind (from, true), temperature, speed and track.

## Predictions

Selecting a report that carried predictions plots them, and selecting any other point replaces them
(with nothing selected, the newest ADS-C route is shown). An ADS-C report draws a dashed line from its
position through the waypoints it expects to reach (with ETAs), plus a ring where the flight computer
expects to be after a fixed time; a CPDLC position report draws a line through its next fixes, leaving
out the one it already passed. Clicking the dot, the waypoint or the plane that stands on a report all
select it. The plane is drawn at the newest trustworthy fix from ADS-B *or* a datalink report, and is
the report itself when that is newer.

ADS-C gives waypoints as coordinates only. Where those coordinates match a waypoint learned from CPDLC
reports, or one in the navigation database, the list adds the name (`ADS-C report ... · next OLGAN,
then KAPPU`) and the tooltip says which. CPDLC position reports list the names they carried. Run
`fullflight -redecode` to give already-stored messages the names.

## Projection

An estimated position now (a grey ghost plane) and a dotted look-ahead, dead-reckoned from the newest
trustworthy fix along its track at its ground speed (or the speed and track worked out from the last
two fixes). Limits: never from a fix over an hour old, never more than 90 minutes beyond the fix, not
for an aircraft on the ground or outside 100-700 kt, and it stops at a known destination (the one you
set, else a recent REP081 one) rather than flying past it. The look-ahead past now (off, 15, 30 or 60 min)
is the selector under the watch list. It does not know about turns or weather deviations.

## Web page

Choosing a watch centres the map (zoom 7) on that aircraft's newest trustworthy
fix, from ADS-B or a datalink message, whichever is later. Each position source has its own track
and colour (ADS-B blue, ADS-C orange, CPDLC purple, REP081 teal, FMS green) with a
legend to switch them on and off. A track is broken, not bridged, across a long
silence (ADS-B 20 min, ADS-C 90, CPDLC 150, REP081 90, FMS 60) or an impossible
jump (over 700 kt), and positions flagged impossible are left out. Click a dot on the map and its message is selected in the list (and scrolled to); click a message that
has a position (orange bar on its left) to ring it on the map and show its popup
(only one popup is ever open); the map pans only if it is out of view. Position messages are listed at the time the report carries, with a dotted underline, and the list is ordered by the time shown; hover for both times and the gap between them (one station logs about 3 minutes before the time inside its messages). Other messages show when they arrived. Hover any message time for its age.
The aircraft is drawn as a plane turned to its track; hovering it shows altitude,
climb/descent trend, ground speed and track. `make uitest` (needs node) drives the
page script against a fake DOM.

Everything drawn shares one longitude frame, anchored at the chosen aircraft's newest fix, so a
track crossing the date line continues from 179 to 181 instead of drawing a line round the world.

## Status

Done: watchlist, ADS-B poller with source fallback, SQLite track log, permanent
hex/tail table, message fetcher and decoder, web UI with map, message timeline
and UTC clock. Not yet: CPDLC reports that name a fix instead of coordinates, flight segmentation
by tail/flight/date, MIAM continuation blocks (shown as "not decoded"),
ADS-C predicted-vs-actual ETA error, SSE live updates, systemd/reverse-proxy docs.
