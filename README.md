# FullFlight

Self-hosted tracker that follows specific aircraft by combining live ADS-B
positions (adsb.lol, adsb.fi) with ACARS/ADS-C/CPDLC datalink messages
(airframes.io), decoded with [golibacars](https://github.com/avanha/golibacars), a Go port of
[libacars](https://github.com/szpajder/libacars). See `HANDOFF.md` for the design and
`docs/airframes-api.md` for API findings.

[![FullFlight tracking an A359 over the Andaman Sea](docs/screenshot.png)](https://avanha.github.io/FullFlight/)

**[Home page](https://avanha.github.io/FullFlight/)**

## Build and run

FullFlight is a pure Go program: the database is SQLite through `modernc.org/sqlite`, and datalink messages are
decoded by [golibacars](https://github.com/avanha/golibacars), a Go port of [libacars](https://github.com/szpajder/libacars).
Building needs only Go, with no C compiler, cmake or system libraries, and it cross-compiles
(`GOOS=linux GOARCH=arm64 go build ./cmd/...`).

- **Go** 1.27.3 or newer (see `go.mod`) from <https://go.dev/dl/>; the version in Debian's own `golang` package is
  usually older. Put it on your `PATH` and check with `go version`.
- Optional: `git`, so the program can name the release it was built from; `nodejs` for the page script tests
  (`node scripts/uitest.js`); and `sqlite3` to look inside the database.

Then:

    go test ./...
    go build -o bin/ ./cmd/...           # -> bin/fullflight
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
- `B-LRM` or `780AB8`: tracks now until `until`, default 36 h. A tail is resolved to its hex so it can be watched
  before it flies. For a **US** registration (`N575UP`), a **Canadian** one (`C-FNOH`, `C-GAZI`) and an **Australian** one
  (`VH-OQA`) the hex is worked out from the registration (and the registration from the hex), with no lookup: those
  countries hand out addresses by a rule (`internal/regs`). Anywhere else the hex comes from what we have already seen, or airframes.io (asked at
  most once an hour per registration).

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
Each has its field elevation (used to judge height above the field when inferring a landing); refresh them with
`scripts/add-airport-elevations.py` and a downloaded copy of OurAirports' `airports.csv`.

Every hex/tail pairing ever seen (from ADS-B or airframes.io) is kept in the
`airframe` table forever, independent of watches; see `GET /api/airframes`.

**What the last ADS-B ask found.** Each card has a line saying when the ADS-B sources were last asked about the
aircraft and what came back, so "why is there no track?" can be answered from the page: `ADS-B req 3m ago · not in the
answer · heard 12m ago (adsb.fi) · 4 asks without it`. The result is one of *heard* (a fresh position, stored), *not in the
answer* (the sources answered and the aircraft was not in it), *in the answer, no usable position* (it was there, without a
position or with one over a minute old: the tooltip says which) and *the ask failed* (no source could answer, shown in
orange, with the error in the tooltip; this is not counted against the aircraft). "Heard" is when the newest position it ever
gave was taken, and by which source; "asks without it" counts since then. A quiet aircraft is asked about only every three
minutes, so "req 3m ago" is normal for one. A paused watch shows when it was last requested. The state is kept per watch in
the database (`poll_state`) and goes with it, so a watch that is removed and added again starts clean. It records the ADS-B
ask only: a position found by flight number through AeroDataBox is not counted as heard here.

**Pausing.** Each watch has a Pause button (`PATCH /api/watches/{id} {"paused": true}`; `false` resumes). A paused
watch is not polled at all (no ADS-B request, no airframes.io fetch, no flight-number lookups), does not run out at
its end time, and stays in the list with its track, messages and everything else we hold, which you can still select and
look at; the plane shows the age of its last position and no projection is drawn. The button is outlined in green once
we have inferred that the aircraft landed, which is the moment to use it. Resume starts polling again (a tail or hex
watch gets its usual 36 hours from then; a flight watch keeps its dated window and cannot be resumed after it). Watching
the same aircraft again resumes the paused entry instead of adding a second. Pausing does not protect the data from
`-purge-older-than`: give the airframe a note for that.

## Finding a flight ADS-B cannot see

With an AeroDataBox key (`aerodatabox.api_key` in `config.yaml`; `config.example.yaml` has the placeholder,
and the real file is git-ignored) a flight watch that ADS-B cannot find, or has lost, is looked up by its
IATA number (CKK624 becomes CK624, via the airline table) and departure date, today then yesterday UTC when
the watch has no date. If the provider has it in the air, the watch latches onto the aircraft's hex and
registration, takes the destination if none was given, and so starts following its datalink messages. The
provider's own position, when it has one, is stored with source `aerodatabox` and drawn as a grey dotted
track. It is only a fallback: with ADS-B working it is never asked, and it never replaces an aircraft the watch
already follows. Each lookup uses your quota, so a watch asks at most every `aerodatabox.every` (30m).
Only flights that are airborne are used; an airline missing from the table needs its ICAO callsign and
cannot be looked up.

The quota is small, so the lookup is spent only where it can help. It is not made for a flight that has landed, nor
for one whose aircraft is known and has not yet left the ground. Once the flight is found under a date, only that
date is asked about from then on (two requests became one). Each request is logged (`flight lookup`, with
`requests_today`), so the log shows what the quota is going on. A 429 from RapidAPI puts every lookup on hold, kept
in the database so a restart does not ask again: for as long as `Retry-After` or the rate-limit reset header says, else
two minutes after a burst or six hours after the plan's quota ("MONTHLY quota" in its message) is spent, never under a
minute or over a day. The log line says whether it was the quota, quotes its message and rate-limit headers, and says
what to check; the flight watches on the page show "flight lookup paused until ...".

Last resort: a flight watch that ADS-B (and AeroDataBox, if set up) cannot find searches airframes.io for
the flight's callsign in the text of recent messages (`text=UPS58`), at most every 10 minutes. Flight plans,
logons and performance reports carry the flight number and name the airframe. The newest message within 3
hours that names it decides the aircraft; yesterday's flight with the same number is older and ignored, and
`UPS580` does not match `UPS58`. airframes.io allows 60 requests per window; this uses one per attempt.

A registration and its ICAO address are one aircraft, so adding a tail watch for an aircraft that already has
an open hex watch (or the other way round) reuses that watch instead of polling it twice. This needs the
pairing to be known (it is kept in the `airframe` table); an unknown tail is its own watch until it is.

## Datalink messages

For every aircraft on an open watch (once its hex is known) the app pulls
messages from airframes.io: 7 days of history on first sight, then every 3
minutes with a 30 minute overlap to catch late uploads. Each is decoded by us
from the raw text (golibacars plus hand parsers), never from airframes.io's own
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
and dotted, and never offered as the aircraft's last position. Such a position is a real one at an unknown
time, so it is not judged "impossible" against where other fixes were at its filing time, and it is not
used as a neighbour to judge others by. (N393DA: a report filed at 21:45Z at 40.1N 111.6W was flown at
17:45Z, as its waypoint time and the ADS-B track both say; the station filed it four hours late.) The newest ADS-C predicted route is drawn dashed.

`GET /api/messages/{hex}?hours=48&limit=300` returns them, oldest first. Airline-
specific engine/performance data (label H1 and others) has no public format; it
is kept raw and hidden in the timeline behind a "show N undecoded" link.
A message nothing decodes is still described as well as its form allows. Readable text (a crew message, a weather forecast,
a company notice, an ATIS) is listed by default as a `text_message` with its first words, and its full text when opened.
Families of maintenance data are named as such ("Real-time system report RPT13", "Software and hardware status table",
"A380 maintenance report REP044", "Ground request for aircraft data", "Acknowledgement") and stay behind "show N
undecoded"; so does a block of numbers, and anything else, which says its label and length. The names come from how the
messages start, in samples: what is inside them is not known.
A message sent in several blocks reaches us as the blocks and usually as one copy joined up (airframes.io stores that under
the last block), and a longer message can be heard first as a shorter one. A block or shorter copy whose text is wholly
inside a longer one with the same message number, received within three minutes, is folded under the whole message: the
list shows one row with a "+2 parts" tag (its hover and its detail say when the others arrived), the count at the top counts
whole messages, and the map still marks every point; clicking a folded point selects the whole message. The API gives
`part_of` on the folded message and `parts` on the whole one. Blocks with no joined copy are left as they are.
Uplink vs downlink is taken from airframes.io when given, else from which end of
the link is the aircraft (`fromHex`/`toHex`), else tried both ways.

After improving the decoder, run `bin/fullflight -config config.yaml -redecode` to
re-decode every stored message from its raw text (idempotent).

### Satellite headers (UPS, Delta)

Over satellite some aircraft put a header of their own in front of the message, so the ADS-C, CPDLC or position
report sits behind text that is not a message number and flight: UPS 747s send downlinks as `F21AUP0081#M1B/B6
ANCATYA.ADS.N575UP...` (`B6` ADS-C, `BA` CPDLC, `B0` AFN), position reports as `D67AUP0081#DFB*POS` and uplinks as `- #MD/A6
OAKODYA.ADS.N575UP...` (`A6`, `AA`, `A0`); Delta's CPDLC downlinks use `#M1B/BA`. The header is taken off, the label inside it
(which says which way the message went) is used, and the message number and flight in front become the message's
flight (UP0081). The result is read as if it had arrived plain, so N575UP's ground-to-aircraft side, the ADS-C contract
requests (Oakland's, then Fukuoka's every 704 s), the CPDLC clearances and handoffs, can be read in the message list,
and its ADS-C and `*POS` reports are on the map: 62 ADS-C, 130 CPDLC and 17 `*POS` messages that were "Undecoded".
A `DIS` message type (ADS-C disconnect?) is not recognised yet. `fullflight -redecode` applies it to stored messages.

### Messages delivered days late

A station can deliver a backlog long after the fact, and the app dates most messages by when they arrive. An ADS-C
report holds only minutes and seconds past the hour, so one from three days ago looks like it is from now: N575UP's
HNL to HKG flight (UPS58) appeared to jump to off northern California when the VDL stations delivered ADS-C and
ACARS from its UPS80 flight of 28 October on the 31st.

- **`*POS` reports** (label H1, UPS 747s: `*POS` then `10281029N3913W12315380278062848M6021298`) carry a full date
  and time: MMDDHHMM in UTC, latitude `DDMM`, longitude `DDDMM`, flight level, wind from (direction, knots), Mach in
  thousandths, temperature (`M` minus, `P` plus), a digit we cannot explain, and a four digit number that falls through
  the flight (fuel, to judge by the rate). Sent on time they match their arrival to the minute, so they are decoded
  and placed at their own time, however late they came: the late ones show up on the 28th, where the flight was, with
  a note saying how late they were delivered.
  Once a flight, on the way down, one more arrives with a ground address in front (`/SDFAD5X.*POS`) and the route
  behind (`...0367TODHKGANC`, airports as IATA codes): shown as `top of descent VHHH→PANC`, and it counts as a route
  report. Of the four we have, all were sent at FL209, so it may be the report for passing that level.
- **POSRPT** (label 80, Delta and Fiji Airways: `3N01 POSRPT 0039/30 KLAX/ZSPD .N501DN`, then `/POS N34414W119598/ALT 252/
  MCH 759/FOB 2022` and `/TME 1941/WND 239 032/OAT -029/TAS 0462/ETA 0925`) is decoded too: position in degrees and
  tenths of minutes, flight level, Mach, wind (a negative direction is 180 to 360, so -142 is 218), temperature, and the
  origin and destination, which count as a route source. `TME` is UTC (174 of the first 206 arrived within five minutes
  of it, the station stamping arrival three minutes early) and the day after the flight number is the day the flight is
  dated, which settles which day when a report is more than a day late (a report more than a day late can still be a
  day out). DAL39's report from Point Conception, delivered at 01:10 on the 31st, is placed at 19:41 on the 30th.
- **China Cargo position reports** (B-221W, B-223A and their sisters) come in two formats, over VDL2 and over satellite
  (behind `M84ACK0624` or `O30ACK0624#10B`). `POSA1` (label 16: `POSA1N59929E149881,DERUD  ,011141,360,OVGAR  ,013635,-50, 46, 391,823`)
  has the position in thousandths of a degree, the waypoint last passed and when (UTC), flight level, the next
  waypoint and its ETA, temperature and wind speed. Its time is the waypoint's, and the position lies between that and
  the next ETA, so a report that arrived inside that window is placed at arrival, and a late one at the middle of the
  window. `SB0122BA_` (H1: `SB0122BA_B-221W  KLAXZSPD355`, then ` 62280-1533462154 35997-420196082...`) has the
  registration and airports, then latitude and longitude, UTC time, altitude in feet, temperature, and wind
  direction and speed, and a time of day only. They are drawn as POSA1 and SB. Over the Pacific they are often all
  there is: the Chinese coast has no ADS-B receivers that feed the sources we ask, and 170 stored messages of 781D8D
  and 781ED4 went from unknown to positions. Run `fullflight -redecode` to apply them to stored messages.
- **They are also clocks.** A message that arrived at the same station within 30 minutes of a `*POS` or POSRPT report
  that was delivered more than an hour late is taken to be from the same backlog (the dated messages split cleanly: 190
  within 10 minutes, 16 between 10 and 60, 45 over an hour late, mostly hours) and flagged as having an
  unverified time, as FMS reports with a doubtful waypoint time are: drawn faint, never the plane's position, not
  projected from, and not naming the aircraft's flight (`in a late batch: ...` in the hover). One nearest to a `*POS`
  on time or only a little late is left alone, and messages from other stations are not judged: live traffic from Honolulu arrives among a
  California backlog. Run `fullflight -redecode` to apply it to stored messages. Progress reports and FMS waypoint
  times are not used as clocks: their times are not UTC in the samples we have.
- Only `*POS` and POSRPT reports are used as clocks so far.

## Destination from the aircraft

Boeing 787 ACMF reports (label H1, header `EB0116AB_9 ,333, N24993,UAL143 , 87,301026,22:53:35,KDEN,RJAA,16591`)
carry a fix every 3 minutes (1 in descent): `N5810.2,W14513.5,302238, 35998,-49.5,215,079,ER,…` is position,
DDHHMM, altitude, temperature, wind from/knots, phase (ER, CL, DC). Only the first block of a long report has
the header, and the message's one position is its newest fix; the others are listed in its detail. Fix times are
whole minutes. The header's trailing number counts seconds (zero point unknown) and a few fields are unexplained.
Airline position reports (label 83, e.g. `M78AZG0001PHNL,RJAA,302314, 28.71, 173.77,40000,258,- 84.1, 62503`) are
sent about every 30 minutes by United and ZIPAIR 787s: message number and flight (optional), origin, destination,
DDHHMM, position in signed decimal degrees, altitude, the direction the wind blows from, the wind along the track
(positive a tailwind), and a number we have not explained. They, the 787 ACMF reports, REP081 and filed flight
plans all say where a flight is going, so any of them gives a watch its route, and a destination to stop the
projection at.
A350 REP081 reports name the flight's origin and destination (`VHHH YSSY`). They come from Cathay and Singapore and
carry one position, or on newer software several a minute apart (the newest is the report's position, the rest are
listed in its detail), in a message that is often compressed. The A350's other MIAM reports have a header with the
tail, the aircraft's own time, the flight and the route, and what raised the report: `A350 report 216: INTEGRATED
REFUEL PANEL DOOR OPEN (SIA23 KJFK→WSSS)`. What they say about the systems (engine and fuel data, parameters) is not
decoded, but the flight and route are used like any other: they count as sightings of the flight and as route reports
(shown as "A350 report"). The XML maintenance report (`<RTR>`) and the takeoff report (`3S06 TAKOFF ...`, with
out, off and ETA times) are read the same way. MIAM acknowledgements are named, and a block that continues a message
too long for one is listed as a data block, with the undecoded messages: its start, with the header, is in an earlier
block (or the whole message in one that airframes.io joined).
Some Delta aircraft send a departure report as they start to taxi (label 4M, or inside a MIAM message): flight number,
origin and destination, and the times of the pushback and the taxi (out event, brake release, movement, engine run, doors
closed). It reads `Departure report flight 87 KLAX→VHHH: out 06:44:43Z, taxi 06:53:01Z` and counts as a route report.
UPS aircraft send three short messages around a departure that name the route in IATA codes (stored as ICAO): label 3J,
a few minutes after takeoff (`010111 31ANC SDF`: sent day-hour-minute UTC, the day of the flight, origin, destination);
label 3N, the pre-departure clearance (`PDC UP0065/31 ANC-SDF`, with the filed route and the proposed departure time);
and label 41, the schedule (`0065 /1031/ANC /SDF /0109/0549/...`: number, date, route, planned departure and arrival).
Each counts as a route report. A 3J delivered long after the time it was sent (stale messages turn up days late)
describes an earlier flight, so it is marked unverified and left out.
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

Each query is answered by the first source that gives a complete answer (a position under 20 s old and a
callsign). An empty or incomplete answer is not final: sources hear different parts of the sky, so the next one is
asked and the results are merged (fresher position wins; a missing callsign, registration or type is filled in from
the other). A source that errors is skipped while another answers.

To share the load, the sources take turns at being asked first (`adsb.spread`, on by default; off means the first
in the list always goes first). `airplanes.live`, which needs its operators' permission, is never in the rotation: it
is only asked after the others.

The feeds rate-limit, so we go gently: at most about one request a second to each source, an aircraft that has not
been heard from for 5 minutes is polled every 3 minutes instead of every 45 s, and a source that answers 401/403
(not allowed) is left alone for an hour, 429 (slow down) for 30 s or its `Retry-After`. Each pause is logged once
with the HTTP status.

Every request to a service (the ADS-B sources, airframes.io, AeroDataBox) carries the User-Agent
`FullFlight/<version> (personal flight tracker; +https://github.com/avanha/FullFlight)`, so an operator can see what is
calling and where to find us. A build from a git checkout names its release: Go records the tag (`v0.6.0`) or, after it, a
pseudo-version such as `v0.6.1-0.20261101123456-abcdef123456+dirty`; a build with no git information says `dev`. The weather imagery (RainViewer, NASA GIBS) is
fetched by your browser, which sends its own.

## Airframes and notes

`/airframes.html` lists every aircraft we know of (searchable, sortable, filterable to those with notes):
its current registration and any earlier ones, hex, type, when first and last heard from, whether it is on a
watch, its **last flight** (`SQ23 KJFK → WSSS`, with when it left, or when it was last heard on that flight; hover for the
callsign and airport names), and a preview of its newest note. An aircraft known only from a watch or a note is listed too. An aircraft that is not being watched has a **Watch**
button: it starts a watch by registration (the usual 36 hours; by hex for an airframe with no registration, or one the watch list would not accept) that then appears on the map page; a refusal is shown at the top.

You can write any number of **notes** about an airframe (`This was the plane Yen took from SEA to HKG on
2026-10-27`), each with its own date, and edit or delete them later. They attach to the hex, the aircraft,
so they stay with it when its registration changes. On the map page every watch with an aircraft has a 📝
icon (faint with none, a count with some; hover for the newest note) that opens a side panel to read, add,
edit and delete its notes; the airframes page opens the same panel. Ctrl/Cmd+Enter adds or saves.

**An airframe with any note is never purged.** That is enforced by the database itself (a trigger refuses
to delete such an airframe, even for a blanket `DELETE FROM airframe`), so it holds for any future
clean-up too. Nothing in the app deletes airframes today. Deleting an airframe's last note makes it
purgeable again.

"Seen" means heard from: the oldest and newest ADS-B position or datalink message we hold for the aircraft
(hover the time for the ADS-B and datalink times separately). Looking up a registration at airframes.io does
not count, and an aircraft with no traffic shows "never heard".

API: `GET /api/airframes`, and `GET`/`POST /api/airframes/{hex}/notes`, `PUT`/`DELETE .../notes/{id}`.

## Storage and purging

Everything is kept by default. Measured: about 1.7 KB per datalink message (decoded text included) and
about 90 bytes per ADS-B position; a busy long-haul aircraft is roughly 100-300 KB per day tracked, so
on-demand use is tens of MB a month, not gigabytes.

    fullflight -stats                              # size, rows per table, oldest/newest, biggest aircraft
    fullflight -purge-older-than 90                # dry run: what would go
    fullflight -purge-older-than 90 -confirm       # delete it, then compact the file

A purge deletes positions and messages older than N days (at least 1) and finished watches. It never touches
an aircraft that has a note, the `airframe` list, notes, waypoints or fetch state. Stop the server first for a
real purge. Decoded detail can be rebuilt with `-redecode`.

## Flights

An aircraft's history is divided into the flights it flew (`GET /api/flights/{hex}`), so a plane with
a week of data does not show as a tangle. A flight is a run of sightings of one flight number
(callsigns on ADS-B and flight numbers in message prefixes, in either form, `CPA113` = `CX113`),
split wherever the number is not seen for six hours, so the same number on two days is two flights. A long stop in
the middle of one is also a split: where ADS-B saw the aircraft land, sit on the ground for 45 minutes or more, and
take off again, the flight is two, each with its own route and departure, even though the number is the same
(UPS65 Hong Kong to Anchorage, then on to Louisville). The cut is half way through the stop, so the arrival's
chatter stays with the first leg and the clearance for the next goes with the second. A stop ADS-B did not see both
ends of is not split.
Stray one-off numbers are folded away. Positions attach to the flight they fall in or within an hour
of. Positions that name no flight but cluster are shown as an *unidentified* flight rather than lost,
and pieces of one flight that report the same REP081 route are joined (a flight whose number was only
logged for its last hours is still one flight).

A flight's **route** is the origin and destination the aircraft reported most often during it (REP081 and the other
A350 reports, ACMF and airline position reports, `*POS` at the top of descent, Delta's departure report and UPS's three route messages, a filed
plan), and **when it left** is the best evidence there is: a takeoff report's takeoff time, else ADS-B seeing it leave
the ground, else a gate time from a departure report. Both are worked out afresh each time, so a flight that has
only its number at first gains its route and departure time as the reports come in.
A message about a flight that has not left yet counts from the time that flight is due to leave, not from when it
arrived. Qantas sends the next flight's data (`FDASUB`, `FDACOM`, with the scheduled time) while the aircraft is still on
its way in, and without this QF1's messages cut QF64 in two just before it landed and gave it QF1's route. The same
rule keeps such a message from naming the flight in progress or its route on the current-flight panel until the flight
is due. The flag at the start of a flight
on the map reads `SQ23 KJFK → WSSS · 31 Oct · dep 15:56Z` (`out 06:44Z` where only the gate time is known), and
its hover gives the airport names and how the departure is known. The aircraft's own hover names its flight, route
and departure the same way.

**Labels are held away from what they name.** A flag, the chosen airport or fix, and the D-ATIS airports below each
sit about 35 pixels up and to the right of their point, joined to it by a line, so they do not hide the route around
it. Drag one anywhere; it keeps that place relative to its point at any zoom, and the browser remembers it (the 200
most recent). Double-click a label to put it back. Hover popups on route points and reports also sit 16 pixels to the side of their point, not on it.

**D-ATIS requests.** A D-ATIS request carries no position, so it is marked on the track with a violet diamond at the
place the aircraft probably was, between the fixes either side of it (the legend entry "D-ATIS requests" switches them off).
Click a diamond, or choose the message in the list, to select it: the airports it asked about then get a violet ring and a
label held away from them (draggable, like the others), and the map zooms out to take them in if they are out of view. It
does this once for each message picked, not again at every refresh. The airports are shown only for the selected message.

**Entering a route by hand.** When the messages never gave a flight's origin or destination, pick the flight in the
**Flights** selector and press **Set route** under it: type the origin and destination (an IATA or ICAO code; the
boxes list matching airports as you type, in the same order as the Airports tab: arrow keys and Enter pick one, and Enter with none highlighted takes the first) and Save. Either end may be left empty. Anything the messages do say wins, so
an end they reported shows in the form but cannot be changed, and once a message supplies an end you entered, the
reported one is shown and your entry is ignored (though kept). Entered ends carry a star where a route is written (`KJFK → VHHH*`, with a note under the selector);
the airframes page shows the star too. An entry belongs to the one flight it was made for (the span it had at the
time) and nothing else: the next day's flight with the same number starts blank. **Clear** removes it.
`PUT /api/flights/{hex}/route` with `{"start": <unix seconds inside the flight>, "from": "KSFO", "to": "VHHH"}` does
the same (400 for a code that is neither a known airport nor four letters or digits; 404 if the aircraft has no
flight then; both empty clears the entry) and returns the flight.

The **Flights** selector under the watch list chooses what the map and message list show: the **most
recent flight** (the default), **all flights**, or any one. In the all-flights view each flight is
drawn on its own (a line never joins two flights), older ones fainter and flagged with their flight
number and date, and the message list has a heading above each flight's messages. Clicking a heading or
a flag shows just that flight and fits the map to it. The plane and the projection appear only for the
flight in progress. The choice of most recent / all is remembered; a particular flight is not.

**A flight that broke in two.** Sightings of one flight number more than six hours apart are normally two flights (the same number
a day later). But a long crossing can be silent for longer, so over a silence of up to 20 hours the pieces are joined if
the aircraft was airborne (above 10,000 ft) at the last fix before it and the first after it, and the speed it would have needed
between them matches the speed it was measured flying, either just before or just after, within 30%, and is between 300 and 650 kt.
(QF8's Pacific crossing on 1 November: 3,960 nm in 8.1 hours is 489 kt, and it was doing 490 kt.) A stop on the ground in the middle
averages far slower than the flying, so two legs, such as UPS81 through Anchorage, are not joined. If the rules cannot tell, choose
the flight and press **Join with the flight before** under the Flights selector; the pieces then show as one flight (starting where the
earlier piece started), and **Split the joined flights** undoes it. A join is kept (`PUT /api/flights/{hex}/join`
with `{"start": unix seconds, "join": true|false}`) against when the later piece started, and goes with it if that flight changes.

**Next-flight chatter.** Airline operations messages (a cockpit asking for the weather along the next route, the airline sending
the next flight's data) can name the next flight while the aircraft is still flying this one. Such a sighting is set aside when the
aircraft itself named another flight within two hours before it and again within two hours after; it counts as before at either end
of a flight (the flight just finished, the one about to start).

**A stale callsign at power-up.** An aircraft that has sat for hours sends its last callsign again when it wakes, and changes it
to the next flight's a couple of seconds later (ZIPAIR's TZP1, then TZP22). Those first sightings, after an hour's silence
and under the old number, are the next flight's when it carries on under the new one at least five times in the next ten
minutes; they are not a late end to the last flight, which would otherwise be stretched over the whole time on the ground and
into the next climb. A chosen flight on the page shows only the points nearest to it, so a point inside the tolerance after
its end that is also inside the next flight's span goes to the next flight.

The page loads the last four days (96 hours) of track, messages and plans with the flight list. The selector also lists
the flights of the last 30 days: those older than four days are in it as well, and choosing one fetches just that flight
(its own hours, from half a day before its start to a few hours after its end, with up to 1000 messages), so a flight from
last week shows in full. "All flights" means the ones in the last four days, and an aircraft that has not flown for a week
shows its latest flight the same way. The month's list is asked for apart and kept for ten minutes, not at every refresh.
`/api/track`, `/api/messages` and `/api/plan` take `until` (RFC 3339) beside `hours` for such a window; `/api/flights?hours=`
goes back up to 30 days.

## Backfilling a track from adsb.lol's archive

ADS-B polling only records what is in the air while an aircraft is being followed. adsb.lol also publishes every day's
tracking data as an archive (<https://github.com/adsblol/globe_history_2026>, one release a day, available from about
03:30Z the next day), and the app can read the track of the aircraft it holds anything for from there.

    backfill:
      enabled: true
      cache_dir: /Volumes/External/FullFlightCache   # must exist; holds adsblol/v2026.10.31-planes-readsb-prod-0.tar.aa ...
      cache_max_gib: 100                             # the oldest days are deleted to stay within this

Nothing is downloaded or read unless `enabled` is on and you start it. The hamburger menu at the top right of the left
panel has **Backfill history...**, which opens its own screen (`/backfill.html`; opened from the map with an aircraft
selected, it starts on that aircraft). The screen lists every day that could be read, with how many aircraft it is for,
whether it is already in the cache or would be downloaded (a day is 3 to 4 GB, in two or three tar parts), and its size.
Tick the days to read (all, none, or only the cached ones are one click each); under the list it says what the chosen
days come to: the download, the cache's room, and which old days would be deleted to make room. Press Start. Each row
shows its progress and, once read, how many positions it gave. Stop ends the run after the file in hand; a half-finished
download is kept and resumed next time. The aircraft can be the one selected or all tracked aircraft.

Which days: for each aircraft, those it has positions or messages on and the day either side, that are already published and
not yet searched for that aircraft. Each day is downloaded once into the cache (parts are written as `name.part` and renamed
when complete and the size GitHub gave, so a file without `.part` is whole), then read once for every aircraft that needs it
(about ten seconds a day). Days you downloaded by hand work too, if the files are named as in the release and placed in
`adsblol/` under the cache directory. `fullflight -backfill 2026-10-31` reads one cached day from the command line (stop the
app first).

Each aircraft's points are added as positions marked `adsb.lol-hist`. A point within ten seconds of a position we already
hold is left out, so overlap with polling is not doubled; points the archive marks stale are left out too. The database
remembers which aircraft were looked for in which day (including those with no trace), so nothing is read twice for them, and
only an aircraft added since needs a day again, from the cache if it is still there. The cache limit never deletes a day a
running backfill still has to read; if it cannot make room it says so and stops. A day that is incomplete (a part missing or
cut short) is refused and nothing is marked. The flights then divide on the new positions as for any others: a flight no
message mentioned now starts where the aircraft really took off.

**When it does not work.** adsb.lol's layout is theirs to change, so every step logs what it saw. Look in the log for lines
starting `backfill:`. On success they give the release found, each part, the download's size and speed, and per day how many
positions were added. On failure they carry a `hint` saying what to check: GitHub's rate limit (60 lookups an hour; the reset
time is logged), no release for the day (not published yet, or the tag naming changed), assets renamed (their names are
listed), the release JSON changed shape, a host that ignored the resume request, a stalled or cut-off download, a part of the
wrong size, an archive with no `traces/xx/trace_full_<hex>.json` files (the first names in it are listed) or a trace file that
is not readable. The tests for each are in `internal/history`.

## Waypoints named in CPDLC reports

A CPDLC position report names the fix just passed, the next fix (with its ETA) and the one after,
but not where they are. Each name gets coordinates from the most trustworthy source that has them:

1. **Learned from the aircraft's own ADS-C** (exact): an ADS-C report lists its next waypoint with
   coordinates and an ETA; a CPDLC fix whose time matches that ETA to within 90 s is that waypoint.
   The fixes after it in the CPDLC list are the waypoints that follow it in that same ADS-C report
   (so the "fix after next", which has no time, is learned too), and a fix passed with no ETA match
   is taken from an ADS-C waypoint change event at that time, a few miles from the waypoint (the
   flight computer sequences it early). Learned positions persist (the `fix` table, with where each
   came from) and name bare ADS-C coordinates later; `fullflight -redecode` learns from stored messages.
2. **The navigation database** (exact), if it fits the flight: not behind the aircraft, no nearer than
   the next fix, within 60 nm of our own estimate. `fullflight -fetch-navdata` downloads FlightGear's
   X-Plane-format `fix.dat.gz`/`nav.dat.gz` into `data/navdata/`. It is GPL data from **2013**, so it
   misses newer waypoints (about half of the names seen on Cathay's Pacific routes) and some names have
   been reused (KADAB is 44 nm behind B-LRD in it); it is kept out of git and the app works without it.
3. **An estimate** (dashed): a passed fix interpolated along the aircraft's own track at the reported
   time; the next fix projected from the report's position, speed and track by its ETA. The fix after
   next has no time, so without 1 or 2 it is not placed.

Solid diamonds are exact, dashed ones estimates; hover for how each was placed.

**Wind barbs.** A report that gives the wind's direction and speed (a 787 ACMF report, an ADS-C meteorological group,
REP081, an FMS position, a CPDLC report with winds) gets a wind barb on its dot, in the source's colour: the staff
points the way the wind comes *from*, with a half barb for each 5 kt, a full barb for 10 and a pennant for 50 (to the
nearest 5 kt), as winds aloft are drawn on upper-air charts. Under 5 kt nothing is drawn: no calm circle. The hover on
the dot gives the numbers: `wind 211°/81kt · -49.6°C`. They can be switched off with "Wind barbs" in the legend. Clicking one selects
the report that named it. Position-report tooltips also show wind (from, true), temperature, speed and track.

## Fix search

The **Fixes** tab in the sidebar lists every waypoint position learned from messages and searches the navigation
database by name. Type to filter (learned names containing the text, database names starting with it; two letters
to search the database); click one, or press Enter for the first, to centre the map on it. A name that is in
use in more than one place is drawn at each. "All on the map" draws every learned fix as a dot.

For each learned fix the list shows how many messages support it, how it was matched (ADS-C predicted ETA, ADS-C
route order, ADS-C waypoint event), **first heard** and **last confirmed** (the receive times of the earliest and
latest message that taught it, not when we processed them), and how far it is from the navigation database's point
of the same name. That last figure is a check on the learning: most agree to 0.01 nm. Where messages disagree
about a name's position within a quarter of a degree, the position most of them agree on is used, not an average.
`GET /api/fixes?q=` serves the same.

## Airport search

The **Airports** tab searches the embedded airport list (large and medium airports and anything with an IATA code)
by code, name or city: an exact code first, then codes that start with what you typed, then names and cities (50
at most; type more to narrow it). With nothing typed it lists the airports your watches use, saying why: a
watch's destination, either end of the route its aircraft reported, or a landing we inferred. Click one (or press
Enter for the first) to centre the map on it with its name, position and field elevation, and a dashed circle of
15 nm, the distance within which a low, slow aircraft that goes silent is taken to have landed there.
`GET /api/airports?q=` serves the same.

## Predictions

Selecting a report that carried predictions plots them, and selecting any other point replaces them
(with nothing selected, the newest ADS-C route is shown). An ADS-C report draws a dashed line from its
position through the waypoints it expects to reach (with ETAs), plus an **FMS forecast** ring: where the flight
computer expects the aircraft to be after a fixed time (ADS-C's intermediate projection); a CPDLC position report draws a line through its next fixes, leaving
out the one it already passed. Clicking the dot, the waypoint or the plane that stands on a report all
select it. The plane is drawn at the newest trustworthy fix from ADS-B *or* a datalink report, and is
the report itself when that is newer.

ADS-C gives waypoints as coordinates only. Where those coordinates match a waypoint learned from CPDLC
reports, or one in the navigation database, the list adds the name (`ADS-C report ... · next OLGAN,
then KAPPU`) and the tooltip says which. CPDLC position reports list the names they carried. Run
`fullflight -redecode` to give already-stored messages the names.

## Airline operations messages

Several airlines send their own movement and operations messages. They are decoded as `ops_report` messages, and the
route and times in them join the evidence for a flight's route and departure.

- **Qantas movements** (labels `80` departure, `81` arrival): `MVA QFA0007/25.VHOQG.YSSY AD0213/0233 EA1725 KDFW` is
  out of the gate 02:13Z, off 02:33Z, ETA 17:25Z at KDFW; `AA2008/2012` is on at 20:08Z, in at 20:12Z. The aircraft
  sends them when a ground station is in range, so one can arrive hours after the event; the time is placed on the
  most recent day that is not after the message arrived.
- **Qantas flight data link** (`RF`): the flight's number, route and scheduled time (`,FDASUB…,SYD,,DFW,,QF007,261025,0205,388,,VH-OQG`)
  appear in the submit, commit and status-request messages. The acknowledgements and replies carry numbers only, which are kept as sent.
- **Air Canada** (`4T`, `4Q`): `ATD`/`PSHBK` (left the gate), `TDX` (out and off), `ARV`/`TAR` (on and in), `KQ1`
  (all four times and the route), and `AGFSR`, an in-flight position report with altitude, phase, temperature and
  wind, which is drawn on the map as an "Airline" position. `4C` is the flight information sent before departure,
  `4M` the load sheet (zero fuel, take-off and fuel weights in tonnes, passengers) and `4P` the dangerous goods notice.
- **Ground events** (`49`, `17`): `01ICCL ACA025/311941CYVRZSPD` names an event, the flight, the time and the route.
  Air Canada, United, China Cargo and Zipair send them. The four-letter event codes are shown as sent: I do not know them.
- **787 engine and performance reports** (United and Air Canada, label `H1`): `/ILNGE7X.CLB006UA N249932UAL177 261024000658KDENLIRF…`,
  `APM … UAL182 KDENEDDF211026232013,` and `<701>IEG … ER …` are maintenance data, but each begins with the flight, the
  route and the whole date and time of the event, and most carry the aircraft's position. They are drawn as "Airline"
  positions, placed when they happened even if they were delivered days late, and they name the flight at that time.
  A take-off report (`TOF`, or `TO` in the others) gives the moment of take-off to the second, which becomes the flight's
  departure time. The other numbers, and the event codes beyond take-off, climb and cruise, are not interpreted.
- **Pre-departure clearances** (`A3`, `A4`, Beijing and Shanghai): the runway, SID and squawk
  (`ACA030 CLRD TO CYVR OFF 36R VIA MUGL7X SQUAWK 0075`), and the status of a request.
- **Weather requests** (`5U`): Qantas asking for D-ATIS, TAF or METAR for a list of airports, and Fiji Airways and Delta's `WXRQ`.

A **D-ATIS reply** sent to the aircraft is decoded as a `weather_report`: "D-ATIS arrival information P for PHNL:
060°/8 kt, 10 SM, FEW022 BKN030 OVC060, 26/22 °C, QNH 29.88". A METAR uplinked to the aircraft (label `3T`) reads the
same way. The weather is not drawn on the map. D-ATIS requests and replies show the airports they name on the map.

A **data link disconnect** (`.DIS.`) is shown as such, with the aircraft's registration; the two-character code in it is kept but not interpreted.

These formats are read from samples, not specifications. Stored messages show them after `fullflight -redecode`.

## Oceanic clearances

A request for an oceanic clearance (`/EGGX.OC1/RCL 030 UAL26-ATSIX/1316 M085F370`: the entry fix and time, Mach and level)
and the centre's reply (`CLA`: the destination, the track, the route from the entry fix, and the level and Mach to hold) are
decoded as `oceanic_clearance`. The cleared route is drawn on the map as its own layer, "Oceanic clearance" in the legend
(dashed teal, a dot on each fix, the track, level and entry in the hover). It is only the route over the ocean, so it is not
treated as the flight's plan, and a filed plan stays the route that is projected along. Fixes given by coordinates
(`62N020W`) are placed as written; named ones from what we have learned of them or the navigation database. Seen so far: one clearance
and two requests, so a form that does not fit is left undecoded. Free text from the crew that mentions a route (a `7B` message)
is shown as text and not plotted: it has no fixed form.

## Predicted winds

Airlines that plan with forecast winds (Air Canada, Qantas and others) exchange them over ACARS, and these are decoded
as `wind_forecast` messages: the aircraft's request (`REQPWI/TS054236,301026/WQ340.360.380:RW26L.UQQ.ORNAI...`, the
levels and fixes it wants), the table sent back (`PWI/WD400,ECOMI,278067,400M54.NATES,...`: for each level, each fix's
wind direction and speed and the temperature, plus any climb and descent winds), and the receipt (`RESPWI/AK`
acknowledged, `AC` accepted, `RJ` rejected). Uplinks arrive in blocks, each starting `- #M` and a character, that can
cut a fix name in two; they are joined, and the last four characters (a checksum) are dropped. The summary reads
"Predicted winds FL400/380/360/340, 25 fixes ECOMI → N59000W170000", and the message text lists every wind. The
formats and the meaning of the receipt codes are read from samples, not a specification. Winds are not drawn on the map.
The fix list is the route the airline expects, which is useful when the flight plan itself was never sent.

## Flight plans

Many FMS units send the filed flight plan (`FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..N25000W170000..TUNTO..IGURU.Q13..`)
and progress reports (`PRG/TS195622,301026/DTVHHH,07R,...,064946/FNUPS58`). They are decoded as `flight plan` and
`progress` messages, and they name the flight, so messages that were unidentified now belong to it. A progress
report whose own timestamp is more than 10 minutes from its arrival is not used to name the flight (a station
may file a report days late).

These are usually sent over VHF ACARS with no hex address, which the by-address fetch never returns. So
each watched aircraft is also searched for by text, every 15 minutes: its registration (logons and
performance reports name the flight), then `FPN/FN<callsign>` for each callsign on its watches and in its
recent messages. Only that aircraft's results are kept.

`GET /api/plan/{hex}` serves the plans, resolved to positions: coordinates as sent, other fixes from the navigation
database (of several places with a name, the one nearest the point before), the airports at the ends. Fixes the
database does not have (it is from 2013) are listed as not placed. Identical copies are reported once, preferring the
most complete (a station may hear part of a message). The plan also gives the watch its origin and destination
(`flight plan` as the source) and the flight its from/to.

On the map the plan of the flight shown is drawn as a dashed line with a dot at each fix (hover for the name, the airway,
when it was filed). It can be switched off in the legend. The projection follows it: an aircraft within 60 nm of the
route and heading roughly along it (70 degrees) is projected along the legs, turning where they turn, and stops at the
destination; otherwise the straight-line projection is used as before. The plan is as filed, not as flown.

## Weather radar

"Weather radar" in the map legend (first entry, off until you tick it, and remembered) lays [RainViewer](https://www.rainviewer.com)'s
global radar composite over the map, in the NEXRAD colours (green to red), at 65% opacity. A label under the clock gives
the time of the frame and its age; the newest frame is fetched when it is switched on and every 5 minutes after. The
browser fetches it straight from RainViewer (the server is not involved), and the map credits it; see RainViewer's
terms for what it may be used for. Radar only exists where there is radar: there is none over the open ocean, so the
oceanic routes show nothing there. Its tiles are made to zoom 7 and are enlarged when you zoom in further.

### Satellite (IR)

Over most of the oceans there is no ground radar, but storms still show in the infrared: the colder the cloud top, the
taller the cloud. "Satellite (IR)" in the legend (also remembered) lays NASA [GIBS](https://earthdata.nasa.gov/gibs)'s
band 13 "clean infrared" imagery over the map at 55% opacity, with the cold, high cloud tops coloured from blue to red
on the grey cloud, from the three geostationary satellites that cover the Pacific and the Americas: Himawari (60E to the
date line: East Asia, Australia, the western Pacific), GOES-West (to 105W: the eastern Pacific, Hawaii) and GOES-East (to
6E: the Americas and the Atlantic). Each draws only the longitudes it sees well, since imagery near a satellite's edge is
stretched into streaks. **Europe, Africa and the western Indian Ocean are not covered.**

GIBS serves frames 10 minutes apart, by time, and the newest is about half an hour old; the newest of each satellite is
found by trying back from now, and found again every 10 minutes. Frames have holes (a tile can be missing from one and
present in the one before), so a missing tile is taken from the previous frame or the one before that. A label under the
clock says which frames are showing and how old the oldest is. Infrared sees cloud tops, not rain: cold high cirrus can look
like a storm and heavy rain from low cloud can be missed. Faint lines between tiles are in the imagery, not a gap.

## Estimated path and ADS-B points

Where the aircraft went quiet and was next heard from, a dotted grey line joins the two fixes: ADS-C handing over to
ADS-B, or a hole in ADS-B coverage. It follows the filed route when the plan covers both fixes (bending at its
waypoints), else it is a straight line; hover for the two fixes, the time and the distance. It is drawn only for a silence of 5
minutes to 3 hours between trustworthy fixes of one flight, never across an impossible speed, and not where the two fixes
are already one continuous track (in the unified view, within the sparser source's usual interval; by source, the same source within its own). It has its own legend entry
("Estimated path").

The ADS-B track has a small hoverable dot every 2.5 minutes and 6 nm or so (a point is kept only if it is at least that far in
time and distance from the last one kept), showing the callsign, time, altitude, speed and track. They are on the ADS-B layer.

## Ghost position for a message with no position

Select a message that carries no position and, if trustworthy fixes (ADS-B or datalink) lie either side of
its time, a faded plane appears where the flight path puts it, interpolated in time along the line between
them; hover for which two fixes it used. It shows only for the selected message. There is none beyond the
last fix (the projection covers that), across a silence of more than 3 hours, between two different
flights, or between fixes no aircraft could fly between.

## Projection

The grey ghost plane appears only once the newest fix is 3 minutes old; a fresher one (ADS-B arrives about
every 45 s) is the plane's position, and only the dotted look-ahead is drawn from it.

An estimated position now (a grey ghost plane) and a dotted look-ahead, dead-reckoned from the newest
trustworthy fix along its track at its ground speed (or the speed and track worked out from the last
two fixes). Limits: never from a fix over an hour old, never more than 90 minutes beyond the fix, not
for an aircraft on the ground or outside 100-700 kt, and it stops at a known destination (the one you
set, else a recent REP081 one) rather than flying past it. The look-ahead past now (off, 15, 30 or 60 min)
is the selector under the watch list. It does not know about turns or weather deviations.

When the flight's filed plan is known and the aircraft is near it, the projection follows the plan's legs, but it starts
from where the aircraft *is*: the distance from the line at the last fix is kept and closed over 150 nm (or by the end
of the route), so the dotted path never begins on the line when the aircraft is beside it. Where the plan's own line is
poor (a fix missing from the 2013 database leaves a long straight leg), the route ahead is shaped by the next and
next-but-one waypoints of the newest ADS-C report (under 45 minutes old, and still ahead of the aircraft), which are
exact, and then rejoins the plan. A fix name's position is taken from what we have learned from aircraft's own
reports (marked `nav db (learned)` in the plan's hover) before the navigation database.

**The plane shows how stale its fix is**, in steps that follow what the projection does with it, with a badge giving
the age of the fix (`7m`, `2.5h`) that keeps counting between refreshes:

| age of the newest fix | plane | meaning |
|---|---|---|
| under 3 min | red | live; no ghost needed |
| 3 min | red, age badge | the estimated-position ghost is drawn ahead of it |
| 15 min | orange-red | the estimate is getting less certain |
| 30 min | amber-brown | the estimate is a long extrapolation |
| 60 min | grey, dashed ring | too old to project from: no ghost, no look-ahead |

The plane's hover says which step it is in. (Whether the look-ahead is switched off is a separate setting.)

**Landing.** Many arrivals drop out of ADS-B coverage on the final approach or the runway, and dead reckoning from
such a fix would fly the aircraft on past the runway. So the server infers a landing, with or without a known
destination: the newest position the aircraft has, from any source, is airborne and under 5,000 ft and 250 kt, or on
the ground; an airport (large, medium or with an IATA code) is within 15 nm and, if the track is known, within 60
degrees of it; and the time to reach it at the fix's speed, plus a minute for a late fix, has passed. A known
destination wins when several airports are near. The watch then carries `landing` (airport, estimated touchdown, the
evidence); the plane turns green with a `landed` badge, the projection is not drawn, and the sidebar and hover say
`landed RJAA ~03:26:44Z`. A newer position of any kind clears it. It is an inference: a missed approach that then
leaves coverage would be called a landing until the aircraft is heard again.

## Web page

The sidebar's Watches tab is split in two, each part folding away like an accordion: **Targets** above (the add
form stays put, the list of watches scrolls on its own) and **Messages** below (with the flight selector and the
projection control above its own scrolling list). Fold one and the other takes the height; the header of a folded
Targets shows the target you have selected, and each header counts what is in it. Your choice is remembered, and
choosing a message on the map opens Messages if it is folded.

Choosing a watch centres the map (zoom 7) on that aircraft's newest trustworthy
fix, from ADS-B or a datalink message, whichever is later. By default one **unified track** runs through every
plausible position, ADS-B and datalink reports together, in time order. Each source keeps its colour (ADS-B blue, ADS-C
orange, CPDLC purple, REP081 teal, FMS green, ACMF crimson): where the source changes, the join takes the first source's
colour up to its midpoint and the next one's from there, so a report is flanked by its own colour on both sides. Positions
flagged impossible, or with an unverified time, are left out, as is a lone fix that no neighbour agrees with, and a report
with ADS-B within 3 minutes on both sides keeps its dot but not its line (ADS-B is denser and more exact). A silence
longer than the sparser of the two sources allows (ADS-B 20 min, ADS-C 90, CPDLC 150, REP081 90, FMS 60, ACMF 40) or
an impossible jump (over 700 kt) ends the solid line; the dotted grey estimate takes over across a silence. The legend
shows each source as its dot with a line out of each side, and has two radio buttons: **Unified track** or **Track by
source** (a line per source in its own dash style, broken at that source's own silences). The choice is remembered. A
source's checkbox shows or hides its dots, and in the unified view takes its fixes out of the line too. Click a dot on the map and its message is selected in the list (and scrolled to); click a message that
has a position (orange bar on its left) to ring it on the map and show its popup
(only one popup is ever open); the map pans only if it is out of view. Position messages are listed at the time the report carries, with a dotted underline, and the list is ordered by the time shown; hover for both times and the gap between them (one station logs about 3 minutes before the time inside its messages). Other messages show when they arrived. Hover any message time for its age.
The aircraft is drawn as a plane turned to its track; hovering it shows altitude,
climb/descent trend, ground speed and track. `node scripts/uitest.js` and `node scripts/uitest-notes.js` (need node) drive the
page script against a fake DOM.

Everything drawn shares one longitude frame, anchored at the chosen aircraft's newest fix, so a
track crossing the date line continues from 179 to 181 instead of drawing a line round the world.

## Status

Done: the watchlist (tails, hex codes, flights and dates), the ADS-B poller with source fallback, the SQLite track log,
the message fetcher and decoder (ADS-C, CPDLC, AFN logons and their acknowledgements, predicted winds, REP081 and the other A350 MIAM reports, other position report
formats, airline satellite headers), the web page (map, message timeline, UTC clock), the waypoints and fixes named in CPDLC reports, the division
of an aircraft's history into flights, flight plans, predictions and the projection, estimated paths, weather radar and
satellite overlays, wind barbs, the unified track, airframe notes, and fix and airport search.

Not yet:

- The contents of the A350 engine, fuel and parameter reports (only their headers are read), the compressed engine files (`EXW…`), and putting continuation blocks of a MIAM message back together (the whole is usually in the message airframes.io joined).
- Comparing an ADS-C report's predicted ETA with the actual passage of the waypoint, and showing the error.
- Live updates pushed to the page (it polls; SSE would replace that).
- Deployment docs: a systemd unit and a reverse proxy example. (HTTP basic auth is built in; see `config.example.yaml`.)
