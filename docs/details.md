# FullFlight in detail

The full description of what FullFlight does, section by section. The [README](../README.md) is the short version, with a
link from each of its points to the part of this page that explains it. `HANDOFF.md` has the design and `airframes-api.md`
(beside this file) the airframes.io API findings.

## Watchlist

Type into the web UI (or `POST /api/watches {"input": "...", "until": "<RFC3339>"}`):

- `UA869 2026-10-31`: waits for the callsign (UAL869) to appear, then follows that aircraft by hex. The window is 12 h
  before the date (UTC) through 60 h.
- `CPA113`, `TZP1`, `CKK624` (no date): three letters then digits is a callsign, whatever the airline, since no registration
  looks like that. It is a flight watch that starts now and runs 36 h (or until `until`); `CPA113 SFO` adds a destination.
  `UA869` (no date) reads the same way for airlines whose two-letter code we know.
- When the shape cannot tell, say so with a prefix: `flight:CK624` (a two-letter number for an airline we have no code for;
  the ICAO callsign always works), `tail:CKK624`, `hex:ABC123`. Six hex digits (`AEE123`) are an ICAO address unless we know
  the airline. The page says how it read each entry, and every watch carries a chip (callsign, registration, hex, flight).
- `B-LRM` or `780AB8`: tracks now until `until`, default 36 h. A tail is resolved to its hex so it can be watched before it
  flies. For a **US** (`N575UP`), **Canadian** (`C-FNOH`) or **Australian** (`VH-OQA`) registration the hex is worked out
  from the registration, and the registration from the hex, with no lookup: those countries hand out addresses by a rule
  (`internal/regs`). Elsewhere the hex comes from what we have already seen, or from airframes.io (at most once an hour per
  registration).

Watching a tail or a hex again once its watch has run out (done or expired) carries on that same watch for another 36 h
(or until `until`), whether typed in the box, started from the Airframes screen, or the **Watch again** button a finished tail
or hex watch shows in the list. An aircraft is listed once, not once for every time it was followed. Adding one that is already being watched says
"Already watching" under the box, and one that was carried on says "Watching again" (the answer to `POST /api/watches` has a `result` of `added`, `existing` or `reopened`). A flight watch cannot be
carried on: its window has passed.

Add an optional destination as its 4-letter ICAO code (the 3-letter IATA code is accepted too): `UA869 2026-10-31 KSFO`.
Routes and aircraft popups show airports by ICAO code; the Airports tab, its search and an airport's own popup show the IATA
code beside it.

- **With a destination**, the watch ends when the aircraft is on the ground within 10 nm of that airport (or was last seen low,
  within 15 nm, and has been silent for 10 min). Landing anywhere else is a stopover or diversion and watching continues, even
  under a new callsign; it ends only after 4 x `poll.stopover_grace` (12 h) on the ground.
- **Without one**, ADS-B gives no destination, so it is inferred from behaviour: the watch ends when the aircraft stays on the
  ground longer than `poll.stopover_grace` (default 3 h) or starts flying under a different callsign. A shorter stop is a
  stopover.

Airport data is OurAirports (public domain), large and medium airports plus any with an IATA code, embedded from
`internal/airports/airports.csv` with each field elevation (used to judge height above the field when inferring a landing).
Refresh the elevations with `scripts/add-airport-elevations.py` and a downloaded `airports.csv`.

Every hex/tail pairing ever seen (from ADS-B or airframes.io) is kept in the `airframe` table forever, independent of
watches; see `GET /api/airframes`.

### What the last ADS-B ask found

Each card says when the ADS-B sources were last asked about the aircraft and what came back, so "why is there no track?" can
be answered from the page: `ADS-B req 3m ago · not in the answer · heard 12m ago (adsb.fi) · 4 asks without it`. The result is
one of:

- *heard*: a fresh position, stored;
- *not in the answer*: the sources answered and the aircraft was not in it;
- *in the answer, no usable position*: it was there without a position, or with one over a minute old (the tooltip says which);
- *the ask failed*: no source could answer. Shown in orange with the error in the tooltip, and not counted against the aircraft.

"Heard" is when the newest position it ever gave was taken, and by which source; "asks without it" counts since then. A quiet
aircraft is asked about only every three minutes, so "req 3m ago" is normal. A paused watch shows when it was last requested.
The state is kept per watch (`poll_state`) and goes with it. It records the ADS-B ask only: a position found through
AeroDataBox does not count as heard.

### Pausing

Each watch has a Pause button (`PATCH /api/watches/{id} {"paused": true}`; `false` resumes). A paused watch is not polled at
all (no ADS-B request, no airframes.io fetch, no flight-number lookups) and does not run out at its end time. It stays in the
list with its track, messages and notes, which you can still select; the plane shows the age of its last position and no
projection is drawn. The button is outlined in green once we have inferred that the aircraft landed, which is the moment to use
it.

Resume starts polling again (a tail or hex watch gets its usual 36 hours from then; a flight watch keeps its dated window and
cannot be resumed after it). Watching the same aircraft again resumes the paused entry instead of adding a second. Pausing does
not protect data from `-purge-older-than`: give the airframe a note for that.

## Finding a flight ADS-B cannot see

With an AeroDataBox key (`aerodatabox.api_key` in `config.yaml`) a flight watch that ADS-B cannot find, or has lost, is looked
up by its IATA number (CKK624 becomes CK624, via the airline table) and departure date: today then yesterday UTC when the watch
has no date. If the provider has it in the air, the watch latches onto the aircraft's hex and registration, takes the
destination if none was given, and so starts following its datalink messages. The provider's own position is stored with
source `aerodatabox` and drawn as a grey dotted track. It is only a fallback: with ADS-B working it is never asked, and it
never replaces an aircraft the watch already follows. Only airborne flights are used, and an airline missing from the table
needs its ICAO callsign and cannot be looked up.

The quota is small, so it is spent only where it can help:

- A watch asks at most every `aerodatabox.every` (2h; the free RapidAPI plan has 400 units a month and a lookup costs two or more, so 30m could use a month's quota on one lost flight), and not for a flight that has landed or whose aircraft is known and
  has not yet left the ground.
- Once the flight is found under a date, only that date is asked about.
- Each request is logged (`flight lookup`, with `requests_today`).
- A 429 from RapidAPI puts every lookup on hold, kept in the database so a restart does not ask again: for as long as
  `Retry-After` or the rate-limit reset header says, else two minutes after a burst or six hours after the plan's quota
  ("MONTHLY quota" in its message) is spent, never under a minute or over a day. The log says which it was, and the flight
  watches show "flight lookup paused until ...".

Last resort: a flight watch that ADS-B (and AeroDataBox) cannot find searches airframes.io for the callsign in the text of
recent messages (`text=UPS58`), at most every 10 minutes and using one of its 60 requests per window. Flight plans, logons and
performance reports carry the flight number and name the airframe. The newest message within 3 hours that names it decides the
aircraft; yesterday's flight with the same number is older and ignored, and `UPS580` does not match `UPS58`.

A registration and its ICAO address are one aircraft, so adding a tail watch for an aircraft that already has an open hex watch
(or the other way round) reuses that watch. This needs the pairing to be known; an unknown tail is its own watch until it is.

## Datalink messages

For every aircraft on an open watch (once its hex is known) the app pulls messages from airframes.io: 7 days of history on first
sight, then every 3 minutes with a 30 minute overlap to catch late uploads. Each is decoded by us from the raw text (golibacars
plus hand parsers), never from airframes.io's own fields. Those are kept as `af_*` hints, and a disagreement (tail, flight
number, or a tail that belongs to a different hex) shows as a warning badge. A message heard by several stations is stored once
(`rx_count`). `GET /api/messages/{hex}?hours=48&limit=300` returns them, oldest first.

A registration of seven characters (`B-16783`) fills the ARINC 622 aircraft address, so the application is followed directly by the
registration (`.AT1B-16783`, not `.AT1.VT-ALN`); these are read as ADS-C and CPDLC like the others.

**Positions.** ADS-C, CPDLC, REP081 and FMS (`POS…`) positions are placed in time from their embedded timestamps and drawn in
orange. One is marked suspect only when it is impossible (over 700 kt) against every one of the two nearest fixes on each side,
so a coherent run of fixes is never condemned by a conflicting one and the result does not depend on fetch order. The newest
ADS-C predicted route is drawn dashed.

**FMS times.** FMS position reports are placed at their receipt time, which is when airframes.io stored them (its `timestamp`
equals its `createdAt`; the receiving station's clock is not read). Their embedded waypoint time agrees with that for some
stations and is hours off for others, for reasons not yet understood. Those more than 10 minutes from arrival are marked ⏱ (time
unverified), drawn faint and dotted, and never offered as the aircraft's last position. Such a position is a real one at an
unknown time, so it is not judged "impossible" against where other fixes were at its filing time, and is not used as a neighbour
to judge others.

**Messages nothing decodes.** Airline-specific engine and performance data (label H1 and others) has no public format; it is
kept raw and hidden behind a "show N undecoded" link. Even so, a message is described as well as its form allows. Readable text
(a crew message, a forecast, a company notice, an ATIS) is listed by default as a `text_message` with its first words. Families
of maintenance data are named as such ("Real-time system report RPT13", "A380 maintenance report REP044", "Ground request for
aircraft data", "Acknowledgement") and stay behind "show N undecoded", as does a block of numbers or anything else (which says
its label and length). The names come from how the messages start, in samples; what is inside is not known.

**Messages in blocks.** A message sent in several blocks reaches us as the blocks and usually as one copy joined up (airframes.io
stores that under the last block); a longer message can also be heard first as a shorter one. A block or shorter copy whose text
is wholly inside a longer one with the same message number, received within three minutes, is folded under the whole message:
the list shows one row with a "+2 parts" tag, the count at the top counts whole messages, and the map still marks every point
(clicking a folded point selects the whole message). The API gives `part_of` on the folded message and `parts` on the whole one.
Blocks with no joined copy are left alone.

**Direction.** Uplink vs downlink is taken from airframes.io when given, else from which end of the link is the aircraft
(`fromHex`/`toHex`), else tried both ways.

After improving the decoder, run `bin/fullflight -config config.yaml -redecode` to re-decode every stored message from its raw
text (idempotent).

### Satellite headers (UPS, Delta)

Over satellite some aircraft put a header of their own in front of the message, so the ADS-C, CPDLC or position report sits
behind text that is not a message number and flight. UPS 747s send downlinks as `F21AUP0081#M1B/B6 ANCATYA.ADS.N575UP...` (`B6`
ADS-C, `BA` CPDLC, `B0` AFN), position reports as `D67AUP0081#DFB*POS` and uplinks as `- #MD/A6 OAKODYA.ADS.N575UP...` (`A6`,
`AA`, `A0`); Delta's CPDLC downlinks use `#M1B/BA`. The header is taken off, the label inside it (which says which way the
message went) is used, and the message number and flight in front become the message's flight (UP0081). The result is read as if
it had arrived plain. A
`DIS` message type is not recognised yet.

### Messages delivered days late

A station can deliver a backlog long after the fact, and the app dates most messages by when they arrive. An ADS-C report holds
only minutes and seconds past the hour, so one from three days ago looks like it is from now: N575UP's HNL to HKG flight (UPS58)
appeared to jump to off northern California when VDL stations delivered ADS-C and ACARS from its UPS80 flight of 28 October on
the 31st. Formats that carry their own full date and time are therefore placed at that time, however late they came (see
[The time a message counts as](#the-time-a-message-counts-as)):

- **`*POS`** (label H1, UPS 747s: `*POS` then `10281029N3913W12315380278062848M6021298`) carries MMDDHHMM in UTC, latitude `DDMM`,
  longitude `DDDMM`, flight level, wind from (direction, knots), Mach in thousandths, temperature (`M` minus, `P` plus), a digit
  we cannot explain, and a four digit number that falls through the flight (fuel, by the rate). Late ones show up where the flight was, with a note saying how late they were delivered. On the way down one more arrives with a ground address in front (`/SDFAD5X.*POS`) and the route behind (`...0367TODHKGANC`, IATA
  codes): shown as `top of descent VHHH→PANC`, and it counts as a route report.
- **POSRPT** (label 80, Delta and Fiji Airways: `3N01 POSRPT 0039/30 KLAX/ZSPD .N501DN`, then `/POS N34414W119598/ALT 252/MCH
  759/FOB 2022` and `/TME 1941/WND 239 032/OAT -029/TAS 0462/ETA 0925`) gives position in degrees and tenths of minutes, flight
  level, Mach, wind (a negative direction is 180 to 360, so -142 is 218), temperature, and the origin and destination, which
  count as a route source. `TME` is UTC and the day after the flight
  number is the day the flight is dated, which settles the day when a report is more than a day late.
- **China Cargo position reports** (B-221W, B-223A and sisters) come in two formats, over VDL2 and satellite (behind
  `M84ACK0624` or `O30ACK0624#10B`). `POSA1` (label 16: `POSA1N59929E149881,DERUD  ,011141,360,OVGAR  ,013635,-50, 46, 391,823`)
  has the position in thousandths of a degree, the waypoint last passed and when (UTC), flight level, the next waypoint and its
  ETA, temperature and wind speed. Its time is the waypoint's and the position lies between that and the next ETA, so a report
  that arrived inside that window is placed at arrival and a late one at the middle of the window. `SB0122BA_` (H1:
  `SB0122BA_B-221W  KLAXZSPD355`, then ` 62280-1533462154 35997-420196082...`) has the registration and airports, then latitude
  and longitude, UTC time, altitude in feet, temperature, and wind, and a time of day only. Over the Pacific they are often all
  there is, since the Chinese coast has no ADS-B receivers feeding our sources.

**They are also clocks.** A message that arrived at the same station within 30 minutes of a `*POS` or POSRPT report delivered
more than an hour late is taken to be from the same backlog (the dated messages split cleanly: 190 within 10 minutes, 16 between
10 and 60, 45 over an hour late, mostly hours). It is flagged as having an unverified time, as FMS reports with a doubtful
waypoint time are: drawn faint, never the plane's position, not projected from, and not naming the aircraft's flight (`in a late
batch: ...` in the hover). One nearest to a `*POS` on time or only a little late is left alone, and messages from other stations
are not judged (live traffic from Honolulu arrives among a California backlog). Only `*POS` and POSRPT are used as clocks so
far; progress reports and FMS waypoint times are not UTC in the samples we have.

Run `fullflight -redecode` to apply any of this to stored messages.

## Destination from the aircraft

Several report types say where a flight is going, so any of them gives a watch its route, and a destination to stop the
projection at:

- **787 ACMF reports** (label H1, header `EB0116AB_9 ,333, N24993,UAL143 , 87,301026,22:53:35,KDEN,RJAA,16591`) carry a fix every
  3 minutes (1 in descent): `N5810.2,W14513.5,302238, 35998,-49.5,215,079,ER,…` is position, DDHHMM, altitude, temperature, wind
  from/knots, phase (ER, CL, DC). Only the first block of a long report has the header, and the message's one position is its
  newest fix; the others are in its detail. Fix times are whole minutes.
- **Airline position reports** (label 83, e.g. `M78AZG0001PHNL,RJAA,302314, 28.71, 173.77,40000,258,- 84.1, 62503`), about every
  30 minutes from United and ZIPAIR 787s: message number and flight (optional), origin, destination, DDHHMM, position in signed
  decimal degrees, altitude, the direction the wind blows from, and the wind along the track (positive a tailwind).
- **A350 REP081** names the flight's origin and destination (`VHHH YSSY`). It comes from Cathay and Singapore and carries one
  position, or on newer software several a minute apart (the newest is the report's position, the rest are in its detail), often
  in a compressed message. The A350's other MIAM reports have a header with the tail, the aircraft's own time, the flight and the
  route, and what raised the report: `A350 report 216: INTEGRATED REFUEL PANEL DOOR OPEN (SIA23 KJFK→WSSS)`. What they say about
  the systems is not decoded, but the flight and route count as sightings of the flight and as route reports ("A350 report"). The
  XML maintenance report (`<RTR>`) and the takeoff report (`3S06 TAKOFF ...`, with out, off and ETA times) are read the same way.
  MIAM acknowledgements are named, and a block that continues a message too long for one is listed as a data block with the
  undecoded messages.
- **Delta departure reports**, sent as the aircraft starts to taxi (label 4M, or inside a MIAM message): flight number, origin and
  destination, and the times of pushback and taxi. It reads `Departure report flight 87 KLAX→VHHH: out 06:44:43Z, taxi 06:53:01Z`.
- **UPS** sends three short messages around a departure that name the route in IATA codes (stored as ICAO): label 3J, a few
  minutes after takeoff (`010111 31ANC SDF`: day-hour-minute UTC, day of the flight, origin, destination); label 3N, the
  pre-departure clearance (`PDC UP0065/31 ANC-SDF`); and label 41, the schedule (`0065 /1031/ANC /SDF /0109/0549/...`). A 3J
  delivered long after the time it was sent describes an earlier flight, so it is marked unverified and left out.
- **Filed flight plans** (see [Flight plans](#flight-plans)).

**Where the route is shown.** The route of the aircraft's newest flight (the one the map's list ends with, see
[Flights](#flights)) appears on the watch (`VHHH → YSSY (REP081 12m ago)`) and in the plane's hover, for tail and hex watches as
well as flights. It is what that flight's reports said most often, with the kind and age of the newest. Where no report gave an
origin, it is the one ADS-B saw it leave (`KLAX~`) or you entered by hand (`KLAX*`). The previous flight's route is never carried
over to the next: an aircraft parked under a new flight number shows that flight's route, or none. REP081 goes out about every 20
minutes in flight, so a report older than 90 minutes is shown greyed with "may be out of date" (and left out of the hover), since
the aircraft may have landed and moved on; after 24 hours it is not shown. If you gave a destination and the aircraft reports a
different one, the line says so. Aircraft of other airlines send no such report, so this appears only where the data exists.

## Current flight

A tail or hex watch follows an aircraft from flight to flight, so it shows the flight it is operating now
(`flight CPA113 · CX113 (from messages 4m ago)`): the newest flight worked out for it (see [Flights](#flights)), named by
whichever is newer, the callsign on its latest ADS-B position or the flight number in its latest message (the 10-character
prefix, or an AFN logon), with the other form worked out from the airline table. A flight its messages have named that has no
position yet (the next flight, announced while the aircraft is at the gate) counts too, and is what a landed aircraft is said to
be waiting to fly (`CPA615 not departed`). It goes grey after 90 minutes and disappears after 24 hours, like the reported
destination. A message whose text names a different aircraft is ignored. The destination is deliberately not guessed from D-ATIS
requests: crews also request them for alternates.

On flight and hex watches the tail has a 📋 button that copies it, so an aircraft first followed by callsign can be re-added by
registration. A double click on any identifier in the row (callsign, registration, hex, and both forms of the flight line) copies
it too and says so. This works where selecting by hand does not: the list is redrawn every few seconds and the selection goes
with it.

## ADS-B sources

Each query is answered by the first source that gives a complete answer (a position under 20 s old and a callsign). An empty or
incomplete answer is not final: sources hear different parts of the sky, so the next one is asked and the results merged (fresher
position wins; a missing callsign, registration or type is filled in from the other). A source that errors is skipped while
another answers. To share the load, the sources take turns at being asked first (`adsb.spread`, on by default). `airplanes.live`,
which needs its operators' permission, is never in the rotation and is asked only after the others.

The feeds rate-limit, so we go gently: requests to a source are at least 1.25 s apart (adsb.fi allows one a second; the rest is margin for network jitter); an aircraft not heard from for 5
minutes is polled every 3 minutes instead of every 45 s; a source that answers 401/403 is left alone for an hour. A 429 is 30 s,
doubled for each 429 in a row (30 s, 1 min, 2 min ... up to an hour) until it answers again, or the `Retry-After` it gives when that is longer
(honoured in full up to 7 days: adsb.lol's keys have day, week and month allowances and say when each lifts), and an hour at once when
its message names a daily limit (adsb.lol's `rate limit exceeded: 1d requests window`, which an API key's used-up allowance gives).
Each pause is logged once with the HTTP status; the others carry the load meanwhile, so a source that is out for a day is asked a
few dozen times, not thousands. The pause is kept in memory, so a restart asks once more.

If you have an adsb.lol API key (from adsb.lol, for feeders or on request), put it in `config.yaml` as `adsb.lol_api_key` and every
request to adsb.lol carries it in the `X-API-Key` header, as in the example adsb.lol gives with a key. It answers without a key today. The key goes to adsb.lol only, and is dropped if a request is redirected to another host. A
key it refuses is answered 401, which pauses adsb.lol for an hour like any other refusal, so check the log for `adsb source refused us`.
The key does not apply to the archive downloads for the backfill, which come from GitHub.

Every request to a service (the ADS-B sources, airframes.io, AeroDataBox) carries the User-Agent `FullFlight/<version> (personal
flight tracker; +https://github.com/avanha/FullFlight)`. A build from a git checkout names its release (the tag, such as
`v0.6.0`, or a pseudo-version after it); a build with no git information says `dev`. The weather imagery (RainViewer, NASA GIBS)
is fetched by your browser, which sends its own.

## Airframes and notes

`/airframes.html`, from the hamburger menu at the top right of the map's left panel, lists every aircraft we know of (searchable,
sortable, filterable to those with notes): its current registration and any earlier ones, hex, type, when first and last heard
from, whether it is on a watch, its **last flight** (`SQ23 KJFK → WSSS`, with when it left or was last heard on that flight;
hover for the callsign and airport names), and a preview of its newest note. An aircraft known only from a watch or a note is
listed too. One that is not being watched has a **Watch** button that starts a watch by registration (the usual 36 hours; by hex
for an airframe with no registration); a refusal is shown at the top.

You can write any number of **notes** about an airframe (`This was the plane Yen took from SEA to HKG on 2026-10-27`), each with
its own date, and edit or delete them later. They attach to the hex, so they stay with the aircraft when its registration
changes. On the map page every watch with an aircraft has a 📝 icon (faint with none, a count with some; hover for the newest)
that opens a side panel to read, add, edit and delete its notes; the airframes page opens the same panel. Ctrl/Cmd+Enter adds or
saves.

**An airframe with any note is never purged.** The database itself enforces it (a trigger refuses to delete such an airframe, even
for a blanket `DELETE FROM airframe`), so it holds for any future clean-up too. Nothing in the app deletes airframes today.
Deleting an airframe's last note makes it purgeable again.

"Seen" means heard from: the oldest and newest ADS-B position or datalink message we hold for the aircraft (hover the time for
each separately). Looking up a registration at airframes.io does not count, and an aircraft with no traffic shows "never heard".

API: `GET /api/airframes`, and `GET`/`POST /api/airframes/{hex}/notes`, `PUT`/`DELETE .../notes/{id}`.

## Recording the radar

Off by default (`weather.record` in the config). RainViewer lists only the last two hours of radar frames, ten minutes apart, so
the weather an aircraft flew through is gone unless it is kept as it happens. Two settings are easily confused:

- `weather.every` (default 30 minutes, 10 to 100) is how often the app **visits** RainViewer to read its list of frames. It
  must stay well under two hours: a visit that fails, with the next two hours or more after the last good one, loses that
  radar for good. Visiting more often costs only one small request for the list, since each tile is downloaded once.
- `weather.step` (default 20 minutes, a multiple of ten up to an hour) is the time between the **frames kept**: with 20
  minutes, those on :00, :20 and :40.

For each kept frame it downloads the zoom-7 tiles (the finest RainViewer serves) within two tiles of where each followed
aircraft was during the minutes that frame stands for (half a step either side). The path comes from the stored positions,
interpolated across a silence of up to an hour and never beyond; an aircraft on the ground has none. A tile already held is
not fetched again. One RainViewer does not have (a 404: no radar coverage there) is recorded too, with no file, so the map can
tell it from a place that was never visited, and it is not asked for again; a tile that is a picture with nothing in it is
marked clear. A download that fails is not recorded, and the next visit asks again. Tiles recorded before this have no such
mark and count as pictures.

Tiles are files under `weather.dir` (`radar/<frame time>/<z>/<x>/<y>.png`); the `wx_tile` table says what is there. The oldest
frames are deleted to stay under `weather.max_mib`. The tiles are the picture the map's radar layer shows (RainViewer now serves
its Universal Blue scheme whatever scheme is asked for: light blue is about 15 dBZ, blue 20 to 30, yellow 35, orange 40, red 45
to 50, pink 55 to 60, white 65 and over, so the colour is rain intensity, not temperature).

It is polite: one request at a time with a pause between, our User-Agent, and a refusal (429 or a server error) ends the visit
and holds off for an hour, remembered across a restart. The index must name a RainViewer host over https. If the app is not
running for more than two hours the radar for that time is lost. The **Past** side of the legend's "Weather radar" row draws them. It shows the frame nearest the
selected message (or the newest position when none is selected), with a badge saying which frame it is and how far it is from that time (`+10m` after it, `-10m` before it, nothing when it is the same time or the frame was reached with the arrows; with no time chosen it says `Latest`); the arrows on the badge step through the recorded frames, and clicking one of the small ADS-B dots on the track (the ones that show a tooltip) draws the radar for that dot's time, so an ADS-B-only flight can be walked along its track (see the time slider below). It draws only the tiles that were recorded, which is
the area around the followed aircraft, and leaves the rest blank (where there was no position for more than an hour, none was recorded: nothing says where the aircraft was). So that a blank place can be understood, the row has an
**Off | Dim | Light** switch (shown in Past, before the Live | Past switch as in the satellite row, remembered, Dim to start with): Dim darkens everything that was not recorded, Light
lightens what was, and both hatch a place RainViewer has no radar for (as over most of the oceans). The shading lies under the radar, so the radar's colours
are unchanged. Where the pointer is over a blank place, a small box slides out under the radar time badge (top right) saying why ("Radar not recorded here", recorded with
no rain, or no radar coverage). A place that was recorded shows nothing, and the box goes after three seconds of a still pointer. It floats, so nothing moves when it
comes and goes, and the tiles take no part in the mouse, so the track and its dots stay clickable. When no frame was recorded within 15 minutes of the time
wanted it says so, and shows nothing, rather than a frame from another time. The server serves the frames
(`GET /api/weather/radar`, a frame's `.../{frame}/tiles` and `.../{frame}/{z}/{x}/{y}.png`) from the weather directory, whether
or not recording is on now. See [the plan](weather-recording-plan.md) for what comes next (a filtered copy of the infrared imagery).

## Storage and purging

Everything is kept by default. Measured: about 1.7 KB per datalink message (decoded text included) and about 90 bytes per ADS-B
position; a busy long-haul aircraft is roughly 100-300 KB per day tracked, so on-demand use is tens of MB a month.

    fullflight -stats                              # size, rows per table, oldest/newest, biggest aircraft
    fullflight -purge-older-than 90                # dry run: what would go
    fullflight -purge-older-than 90 -confirm       # delete it, then compact the file

A purge deletes positions and messages older than N days (at least 1) and finished watches. It never touches an aircraft that has
a note, the `airframe` list, notes, waypoints or fetch state. Stop the server first for a real purge. Decoded detail can be
rebuilt with `-redecode`.

## Flights

An aircraft's history is divided into the flights it flew (`GET /api/flights/{hex}`), so a plane with a week of data does not
show as a tangle.

### How an aircraft's history is divided

A flight is a run of sightings of one flight number (callsigns on ADS-B and flight numbers in message prefixes, in either form:
`CPA113` = `CX113`), split wherever the number is not seen for six hours, so the same number on two days is two flights.

- **A long stop is a split.** Where ADS-B saw the aircraft land, sit on the ground for 45 minutes or more, and take off again,
  the flight is two, each with its own route and departure, even though the number is the same (UPS65 Hong Kong to Anchorage,
  then on to Louisville). The cut is half way through the stop, so the arrival's chatter stays with the first leg and the
  clearance for the next goes with the second. A stop ADS-B did not see both ends of is not split.
- **Strays** (one-off numbers) are folded away.
- **Positions** attach to the flight they fall in or within an hour of. Positions that name no flight but cluster are shown as an
  *unidentified* flight rather than lost.
- **Pieces of one flight** that report the same REP081 route are joined (a flight whose number was only logged for its last hours
  is still one flight).

### A flight's route and departure

A flight's **route** is the origin and destination the aircraft reported most often during it. The sources are REP081 and the
other A350 reports, ACMF, satellite (`SB...`) and airline position reports, `*POS` at the top of descent, Delta's departure
report, UPS's three route messages, and a filed plan. **When it left** is the best evidence there is: a takeoff report's takeoff
time, else ADS-B seeing it leave the ground, else a gate time from a departure report. Both are worked out afresh each time, so a
flight that has only its number at first gains its route and departure time as the reports come in.

A message about a flight that has not left yet counts from the time that flight is due to leave, not from when it arrived.
Qantas sends the next flight's data (`FDASUB`, `FDACOM`, with the scheduled time) while the aircraft is still on its way in;
without this rule QF1's messages cut QF64 in two just before it landed and gave it QF1's route. The same rule keeps such a
message from naming the flight in progress, or its route, on the current-flight panel until the flight is due.

The flag at the start of a flight on the map reads `SQ23 KJFK → WSSS · 31 Oct · dep 15:56Z` (`out 06:44Z` where only the gate
time is known); its hover gives the airport names and how the departure is known. The aircraft's own hover names its flight,
route and departure the same way.

### Labels are held away from what they name

A flag, the chosen airport or fix, and the D-ATIS airports each sit about 35 pixels up and to the right of their point, joined to
it by a line, so they do not hide the route around it. Drag one anywhere; it keeps that place relative to its point at any zoom,
and the browser remembers it (the 200 most recent). Double-click a label to put it back. Hover popups on route points and reports
also sit 16 pixels to the side.

### D-ATIS requests

A D-ATIS request, or a Qantas request for METARs or TAFs (`Weather request (METAR)`, which are treated the same way), carries no position, so it is marked on the track with a violet diamond where the aircraft probably was, between
the fixes either side of it (the legend entry "D-ATIS, METAR and TAF requests" switches them off). Click a diamond, or choose the message in the
list, to select it: the airports it asked about then get a violet ring and a draggable label, and the map zooms out to take them
in if they are out of view (once for each message picked, not at every refresh). The airports are shown only for the selected
message.

### Entering a route by hand

When the messages never gave a flight's origin or destination, pick the flight in the **Flights** selector and press **Set
route**. Type the origin and destination (an IATA or ICAO code; the boxes list matching airports as you type, in the Airports
tab's order; arrow keys and Enter pick one, and Enter with none highlighted takes the first) and Save. Either end may be left
empty.

- Anything the messages do say wins: an end they reported shows in the form but cannot be changed, and once a message supplies an
  end you entered, the reported one is shown and your entry is ignored (though kept).
- Entered ends carry a star where a route is written (`KJFK → VHHH*`, with a note under the selector); the airframes page shows
  it too.
- An entry belongs to the one flight it was made for and nothing else: the next day's flight with the same number starts blank.
  **Clear** removes it.

`PUT /api/flights/{hex}/route` with `{"start": <unix seconds inside the flight>, "from": "KSFO", "to": "VHHH"}` does the same (400
for a code that is neither a known airport nor four letters or digits; 404 if the aircraft has no flight then; both empty clears
the entry) and returns the flight.

### The Flights selector

The **Flights** selector under the watch list chooses what the map and message list show: the **most recent flight** (the
default), **all flights**, or any one. In the all-flights view each flight is drawn on its own (a line never joins two flights),
older ones fainter and flagged with their number and date, and the message list has a heading above each flight's messages.
Clicking a heading or a flag shows just that flight and fits the map to it. The plane and the projection appear only for the
flight in progress. The choice of most recent / all is remembered; a particular flight is not.

### A flight that broke in two

Sightings of one flight number more than six hours apart are normally two flights. But a long crossing can be silent for longer,
so over a silence of up to 20 hours the pieces are joined if the aircraft was airborne (above 10,000 ft) at the last fix before
it and the first after it, and the speed it would have needed between them is between 300 and 650 kt and within 30% of the speed
it was measured flying just before or just after. (QF8's Pacific crossing on 1 November: 3,960 nm in 8.1 hours is 489 kt, and it
was doing 490 kt.) A stop on the ground averages far slower than the flying, so two legs, such as UPS81 through Anchorage, are not
joined.

If the rules cannot tell, choose the flight and press **Join with previous flight** under the Flights selector; the pieces then
show as one flight (starting where the earlier piece started), and **Split the joined flights** undoes it. A join is kept
(`PUT /api/flights/{hex}/join` with `{"start": unix seconds, "join": true|false}`) against when the later piece started, and goes
with it if that flight changes.

### A stop ADS-B never saw

A through-flight (QF1 from Sydney to Singapore and on to London) keeps one number for both legs. When ADS-B saw the aircraft sit
on the ground between them, the flight is cut there. When it saw nothing of the first leg, and the second shows only as a
climb-out ten hours after the last fix near Sydney, the climb-out is the evidence: a fix at least three hours after the one
before, below 6,000 ft and climbing, within 8 nm of an airport that the earlier fix was over 40 nm from, and with the time to have
got there at 450 kt. The flight is then two: the first keeps the route its messages gave (YSSY to WSSS), and the second, which
has none, gets its origin from the airport it was seen leaving, shown with a tilde (`WSSS~`). A route entered by hand replaces a
seen origin. The same holds when the fix before was the approach itself (low, within 8 nm, descending, as the backfill can supply) and
nothing was heard for an hour or more: it landed, and the climb-out from that airport is the next flight. Where there is no
such climb-out (an aircraft last seen circling near the airport, or first seen at cruise height) the flight is left whole.

The next fix may also be on the ground instead of a climb-out: an arrival heard only by messages (an FMS report on final) and then
ADS-B at the gate, an hour or more later, within 4 nm of the same airport. That is a landing too, and the piece before it is never
joined to the flight after it, even when it has no number of its own (N956AK's AS485 into Seattle and the AS421 that left it).
Where fixes are sparse, one fix below 3,000 ft and within 3 nm of the airport counts as an approach without an earlier, higher one.

### A number only airframes.io gave

An FMS position report carries no flight number in its text, so an arrival heard only that way (N956AK's AS485 into Seattle) has
no sighting to name it and is listed as unidentified. airframes.io reads a number from the ACARS header of such messages
(`AS0485`). When at least two of a flight's messages carry one, all the same, it is shown as the flight's number (`flight_from_af`
in the API). It is only a label: the flight stays unidentified, and the tag never starts, extends, splits or joins a flight, because
airframes.io sometimes tags the flight before or after (3% of messages here disagree with their own text). Messages with an
unverified time, a zero number (`AS0000`), a mismatch note or an arrival over ten minutes after their time are not counted, and two
different tags leave the flight unnamed. A replay over every stored aircraft named 34 of 47 unnumbered flights and moved no boundary.

### Seen taking off

A flight whose messages gave no origin, but whose first fix is ADS-B on the ground within 4 nm of an airport and which is then
seen in the air, left from that airport; the route shows it with the same tilde (`KLAX~`). A flight first seen in the air, or on
the ground away from any airport, or only ever on the ground, gets none.

### Seen landing

A flight whose messages gave no destination, whose last fix is ADS-B on the ground within 4 nm of an airport after being seen in
the air, ended there; failing that, a flight followed by another leg after a stop went to where that leg began. The route shows
it with a tilde (`KDEN~`). This is how the first leg of a flight number reused on the way back (AS421 Seattle to Denver, then
Denver to Seattle) gets its destination. A route entered by hand replaces it, and a message that names a destination wins.

### Next-flight chatter

Airline operations messages (a cockpit asking for the weather along the next route, the airline sending the next flight's data)
can name the next flight while the aircraft is still flying this one. Such a sighting is set aside when the aircraft itself named
another flight within two hours before it and again within two hours after; it counts as before at either end of a flight (the
flight just finished, the one about to start).

### Why a flight looks the way it does

`GET /api/flights/{hex}/explain` (query `hours`, default 96) gives the same flights as `/api/flights/{hex}` with the reasoning for
each, in a `why` list in the order the work was done:

- each run of flight-number sightings and how many positions it took;
- sightings set aside (the old number sent again at power-up, an airline message about the next flight) and strays folded in;
- pieces joined across a long silence, and the speeds that justified it;
- the stops a flight was cut at (seen on ADS-B, or implied by a climb-out);
- the route reports it took and what most of them said, and where an origin no message gave was taken from;
- why two pieces of one number were or were not joined, hand-entered routes and joins, and the departure.

A note that belongs to no flight (a number heard once with no position near it) is listed apart under `unplaced`. The usual list
carries none of it and is not slowed by it.

The map page shows it: with an aircraft selected, **Why are the flights this way?** under the Flights selector opens
`/explain.html?hex=...` in a new tab, with the chosen flight (if one is chosen) opened and marked. The page lists the aircraft's
flights newest first, each folded to its route, span and departure with its notes underneath (span, stage, rule, what was done),
and the notes that belong to no flight at the end. A selector sets how far back to look (4 to 30 days).

### The time a message counts as

Every message has two times: when it arrived, and the time it counts as for the flight it belongs to. The second is, in order:

- the time the aircraft put in it where that is a full date and time: an A350 or RTR report's own time, an operations event's
  time, or its day of the month and time of day (placed in the month it arrived or the one before), a REP081's seconds into the
  month, or the position time of an ACMF, AOC, `*POS` or POSRPT report, or of an airline's position report (a route it names does not count when its position has only a time of day and it arrived over 12 hours later: which day it was made on is a guess);
- for a message about a flight that has not left yet, when that flight is due to leave;
- otherwise the arrival. A message with no time of its own (ATIS requests, CPDLC, AFN logons and the like) can only count from
  its arrival.

It is worked out once, when the message is stored or decoded again (`-redecode`), and kept in the message row with the basis it
rests on (`event`, `scheduled` or `receipt`), so a rule change reaches stored messages the same way a decoder change does.
Everything that places a message in a flight reads that one time, and `/api/messages` takes its window by it and says `at` on a
message whose time differs from its arrival. A flight's messages delivered days late are then the flight's own, and no longer make
a flight of their own in the middle of the next one.

### Reports delivered late

An A350 keeps its reports and sends them later: of the ones that name a route, about half reach us more than a day after the
event. Each counts for the flight it happened during. One that happened at a moment none of our flights holds (the flight itself
is only known from the messages that arrived late) counts for the flight it arrived in. Without this, a route report for one
flight, delivered during the next, gave the next the wrong route (CX833 from Seattle labelled with CX301's Hong Kong to Munich).
Flights known only from late messages, with few or no positions, can therefore show no route; backfilling their day gives them
positions, and their reports their route.

The flights are always worked out from a week before the window asked for, and the ones that end before the window are left out.
A flight that began before the page's last four days is therefore whole, with the same identity (and the same entry in the
Flights selector) as in the month's list, and the earlier flight a late report belongs to is known even when it is outside the
window.

### Why late

About half of the VHF messages that carry the aircraft's own time arrive more than an hour after it; none of the satellite ones
do (365 checked). The late ones come from particular volunteer stations, some of them always, and the lag is neither a wrong clock
nor one backlog flushed after an outage. We cannot see inside the feeders; replay of a recording is a likely cause. The
[Stations](#stations) page measures it per station.

### Messages tagged with another flight

A message with no time of its own (an ATIS request, a CPDLC exchange) lands in whatever flight was in progress when it arrived.
Over VHF that is often wrong: volunteers' stations sometimes upload hours or days late (B-LRD's D-ATIS requests for Munich and
Vienna, from its flight there, showed up during its flight to Hong Kong, delivered by a Slovak station about 44 hours late).
airframes.io tags each message with the flight number from its header. When that tag names another flight of the aircraft that we
have, which ended before this message arrived, `/api/messages` says `elsewhere` (the flight tagged, and its segment). The page
greys the message, shows `→ CX0301` on it, and leaves it out of the chosen flight's list and its ATIS markers. It stays in the
all-flights list, since its true time is not known.

This is done only for VHF messages: a satellite station stamps a message as it arrives, so there a different tag is the aircraft's
own flight number lagging, or the next flight's data, and the time is right. A tag that names a flight due again within a day and
a half is left alone, as that may be the flight to come.

### A stale callsign at power-up

An aircraft that has sat for hours sends its last callsign again when it wakes, and changes it to the next flight's a couple of
seconds later (ZIPAIR's TZP1, then TZP22). Those first sightings, after an hour's silence and under the old number, are the next
flight's when it carries on under the new one at least five times in the next ten minutes; they are not a late end to the last
flight, which would otherwise be stretched over the whole time on the ground and into the next climb. A chosen flight on the page
shows only the points nearest to it, so a point inside the tolerance after its end that is also inside the next flight's span goes
to the next flight.

### How much history the page loads

The page loads the last four days (96 hours) of track, messages and plans with the flight list. The selector also lists the
flights of the last 30 days: older ones are in it too, and choosing one fetches just that flight (from half a day before its
start to a few hours after its end, with up to 1000 messages). "All flights" means the ones in the last four days, and an
aircraft that has not flown for a week shows its latest flight the same way. The month's list is asked for apart and kept for ten
minutes. `/api/track`, `/api/messages` and `/api/plan` take `until` (RFC 3339) beside `hours` for such a window;
`/api/flights?hours=` goes back up to 30 days.

## Stations

Every message came to us through a receiving station: a volunteer's VHF or HF receiver, or one of airframes.io's satellite
receivers. `/stations.html`, from the hamburger menu, lists the ones ours came through (about two hundred) with what they carry
and how late it arrives, beside a map of where they are.

For each station: its name, country and nearest airport, the link and decoder (VHF VDL2 by dumpvdl2, ACARS by acarsdec, Inmarsat
by jaero, ...), how many of our messages came through it for how many aircraft, and when we last heard from it. Then its
delivery, worked out from the messages that carry the aircraft's own time: how many there are, what share arrived over an hour
after the time inside them, the median delay, the 90th percentile, and the shortest delay of any. A station whose shortest delay
is hours long never delivers promptly. The verdict is **late** for half or more late, **sometimes** for some, **prompt** for none,
and **?** for fewer than five timed messages (CPDLC, AFN logons and ATIS requests carry no time, so a station that only delivers
those cannot be judged).

Click a heading to sort; the filters take the link, the verdict, a name, and "on the map only". The divider between the table and
the map can be dragged (or moved with the arrow keys), is remembered, and a double-click puts it back. On a narrow window the map
goes under the table.

Positions and details (country, decoder, whether airframes.io has flagged the station) come from airframes.io's list of
stations: one request of some eight thousand stations and a few megabytes, made by the **Fetch positions** button and not more
than every six hours. Only the stations we have messages from are kept. A station that asked airframes.io to blur its position is
shown at the blurred position and marked approximate; some have no position. airframes.io's "rate anomaly" flag is shown beside
our own verdict but is not a reliable guide to lateness, which is why the delivery is measured here. The page only reports;
nothing in how messages are handled depends on it.

## Stats

`/stats.html`, from the hamburger menu, shows how the app is doing, and refreshes itself every 30 seconds.

- **Requests to other services**, by host: today, yesterday, the last seven days, this month, in all, how many failed (no answer, or
  an HTTP error) and how many were refused with a 429, and the first day counted. Every request the server itself makes is counted
  (airframes.io, the ADS-B sources, AeroDataBox, the OpenStreetMap tile cache, the radar recorder, the backfill downloads), in the
  database, so the counts survive a restart. Requests your browser makes itself (the map's own tiles when the tile cache is off,
  weather layers) go from the browser and are not counted. A service we have been told to leave alone (a 429 hold) is shown above
  the table with when it ends. AeroDataBox's row says what a lookup costs: probably 2 of the plan's 400 monthly units on the free
  plan, so its "this month" count is a request count, not units, and its month may not start when yours does.
- **What we hold**: messages and positions in all, in the last day and the last seven days; how many messages we could read into a
  kind; the same per day for a week; messages by link (VHF, satellite, ...) and positions by source (each ADS-B source, the
  backfill, AeroDataBox); watches by status, known airframes, notes and receiving stations.
- **Busiest aircraft** by messages, and **disk**: each table's rows and size, the OpenStreetMap tile cache and the recorded radar
  against their limits.

The size of the database is measured every five minutes at most, because reading every page holds up the writers for a moment (half
a second for 70 MB). The counts start from the day this feature was installed; the table is `request_count`.

## Backfilling a track from adsb.lol's archive

ADS-B polling only records what is in the air while an aircraft is being followed. adsb.lol also publishes every day's tracking
data as an archive (<https://github.com/adsblol/globe_history_2026>, one release a day, available from about 03:30Z the next day),
and the app can read the track of the aircraft it holds anything for from there.

    backfill:
      enabled: true
      cache_dir: /Volumes/External/FullFlightCache   # must exist; holds adsblol/v2026.10.31-planes-readsb-prod-0.tar.aa ...
      cache_max_gib: 100                             # the oldest days are deleted to stay within this

Nothing is downloaded or read unless `enabled` is on and you start it. The hamburger menu has **Backfill history...**, which
opens its own screen (`/backfill.html`). It starts on all tracked aircraft; one can be chosen in the list at the top, or by adding
`?hex=` and its address to the link.

- The screen lists every day that could be read, with how many aircraft it is for, whether it is already in the cache or would be
  downloaded (a day is 3 to 4 GB, in two or three tar parts), and its size. Days already read for all their aircraft stay listed,
  greyed and not tickable, so what the cache holds can be seen.
- Tick the days to read (all, none, or only the cached ones are one click each). Under the list it says what the chosen days come
  to: the download, the cache's room, and which old days would be deleted to make room.
- Press Start. Each row shows its progress and, once read, how many positions it gave. Stop ends the run after the file in hand; a
  half-finished download is kept and resumed next time.

**Which days.** For each aircraft, those it has positions (polled, not backfilled) or messages on and the day either side, that
are already published and not yet searched for that aircraft. Positions the backfill added do not count, or each day read would
make the day before it worth reading, one at a time, for as long as the aircraft flew.

**Reading.** Each day is downloaded once into the cache (parts are written as `name.part` and renamed when complete and the size
GitHub gave, so a file without `.part` is whole), then read once for every aircraft that needs it (about ten seconds a day). Days
you downloaded by hand work too, if named as in the release and placed in `adsblol/` under the cache directory.
`fullflight -backfill 2026-10-31` reads one cached day from the command line (stop the app first).

**What it adds.** Each aircraft's points are added as positions marked `adsb.lol-hist`. A point within ten seconds of a position we
already hold is left out, so overlap with polling is not doubled, and so are points the archive marks stale. The database
remembers which aircraft were looked for in which day (including those with no trace), so nothing is read twice for them; only an
aircraft added since needs a day again, from the cache if it is still there. The cache limit never deletes a day a running
backfill still has to read; if it cannot make room it says so and stops. A day that is incomplete (a part missing or cut short) is
refused and nothing is marked. The flights then divide on the new positions as for any others: a flight no message mentioned now
starts where the aircraft really took off.

### When it does not work

adsb.lol's layout is theirs to change, so every step logs what it saw: look for lines starting `backfill:`. On success they give
the release found, each part, the download's size and speed, and per day how many positions were added. On failure they carry a
`hint` saying what to check: GitHub's rate limit (60 lookups an hour; the reset time is logged), no release for the day (not
published yet, or the tag naming changed), assets renamed (their names are listed), the release JSON changed shape, a host that
ignored the resume request, a stalled or cut-off download, a part of the wrong size, or an archive with no
`traces/xx/trace_full_<hex>.json` files (the first names in it are listed) or a trace file that is not readable. The tests for
each are in `internal/history`.

## Waypoints named in CPDLC reports

A CPDLC position report names the fix just passed, the next fix (with its ETA) and the one after, but not where they are. Each
name gets coordinates from the most trustworthy source that has them:

1. **Learned from the aircraft's own ADS-C** (exact). An ADS-C report lists its next waypoint with coordinates and an ETA; a
   CPDLC fix whose time matches that ETA to within 90 s is that waypoint. The fixes after it in the CPDLC list are the waypoints
   that follow it in that same ADS-C report (so the "fix after next", which has no time, is learned too), and a fix passed with
   no ETA match is taken from an ADS-C waypoint change event at that time, a few miles from the waypoint (the flight computer
   sequences it early). Learned positions persist (the `fix` table, with where each came from) and name bare ADS-C coordinates
   later; `fullflight -redecode` learns from stored messages.
2. **The navigation database** (exact), if it fits the flight: not behind the aircraft, no nearer than the next fix, within 60 nm
   of our own estimate. `fullflight -fetch-navdata` downloads FlightGear's X-Plane-format `fix.dat.gz`/`nav.dat.gz` into
   `data/navdata/`. It is GPL data from **2013**, so it misses newer waypoints (about half of the names on Cathay's Pacific
   routes) and some names have been reused (KADAB is 44 nm behind B-LRD in it). It is kept out of git and the app works without it.
3. **An estimate** (dashed): a passed fix interpolated along the aircraft's own track at the reported time; the next fix projected
   from the report's position, speed and track by its ETA. The fix after next has no time, so without 1 or 2 it is not placed.

Solid diamonds are exact, dashed ones estimates; hover for how each was placed. A report's fixes are drawn only while that
report is the selected message.

An FMS position report (`POSN…`) names its fixes the same way (`SANDY` passed, `PIKIL` next with its ETA, then a half-degree position)
and they are looked up the same way, in the navigation database near the aircraft, so UA178's PIKIL is plotted at 56N 15W and not at
the Western Australian PIKIL. The report has no speed or track to project a missing fix by, so one the database lacks is not placed,
and its times are not used to learn coordinates from ADS-C, since FMS times are not trusted. Stored reports show their fixes after
`fullflight -redecode`.

### Wind barbs

A report that gives the wind's direction and speed (a 787 ACMF report, an ADS-C meteorological group, REP081, an FMS position, a
CPDLC report with winds) gets a wind barb on its dot, in the source's colour. The staff points the way the wind comes *from*, with
a half barb for each 5 kt, a full barb for 10 and a pennant for 50 (to the nearest 5 kt), as on upper-air charts. Under 5 kt
nothing is drawn. The hover on the dot gives the numbers: `wind 211°/81kt · -49.6°C`. Switch them off with "Wind barbs" in the
legend. Clicking one selects the report that named it.

## Fix search

The **Fixes** tab in the sidebar lists every waypoint position learned from messages and searches the navigation database by
name. Type to filter (learned names containing the text, database names starting with it; two letters to search the database);
click one, or press Enter for the first, to centre the map on it. A name in use in more than one place is drawn at each. "All on
the map" draws every learned fix as a dot.

For each learned fix the list shows how many messages support it, how it was matched (ADS-C predicted ETA, ADS-C route order,
ADS-C waypoint event), **first heard** and **last confirmed** (the receive times of the earliest and latest message that taught
it), and how far it is from the navigation database's point of the same name; most agree to 0.01 nm. Where messages disagree
about a name's position within a quarter of a degree, the position most of them agree on is used, not an average.
`GET /api/fixes?q=` serves the same.

## Airport search

The **Airports** tab searches the embedded airport list (large and medium airports and anything with an IATA code) by code, name
or city: an exact code first, then codes that start with what you typed, then names and cities (50 at most). With nothing typed it
lists the airports your watches use, saying why: a watch's destination, either end of the route its aircraft reported, or a
landing we inferred. Click one (or press Enter for the first) to centre the map on it with its name, position and field
elevation, and a dashed circle of 15 nm, the distance within which a low, slow aircraft that goes silent is taken to have landed
there. `GET /api/airports?q=` serves the same.

## Predictions

Selecting a report that carried predictions plots them, and selecting any other point replaces them (with nothing selected, the
newest ADS-C route is shown).

- An ADS-C report draws a dashed line from its position through the waypoints it expects to reach (with ETAs), plus an **FMS
  forecast** ring: where the flight computer expects the aircraft to be after a fixed time.
- A CPDLC position report draws a line through its next fixes, leaving out the one it already passed.
- Clicking the dot, the waypoint or the plane that stands on a report all select it. The plane is drawn at the newest trustworthy
  fix from ADS-B *or* a datalink report, and is the report itself when that is newer.

An aircraft with no route to give (on the ground, or after landing) fills the waypoints with "not available", which reads as 180S 180W; a
waypoint off the Earth like that is left out, so such a report has no predicted route.

ADS-C gives waypoints as coordinates only. Where those match a waypoint learned from CPDLC reports, or one in the navigation
database, the list adds the name (`ADS-C report ... · next OLGAN, then KAPPU`) and the tooltip says which. Run
`fullflight -redecode` to give already-stored messages the names.

## Airline operations messages

Several airlines send their own movement and operations messages. They are decoded as `ops_report` messages, and the route and
times in them join the evidence for a flight's route and departure.

- **Qantas movements** (labels `80` departure, `81` arrival): `MVA QFA0007/25.VHOQG.YSSY AD0213/0233 EA1725 KDFW` is out of the
  gate 02:13Z, off 02:33Z, ETA 17:25Z at KDFW; `AA2008/2012` is on at 20:08Z, in at 20:12Z. One can arrive hours after the event,
  so the time is placed on the most recent day that is not after the message arrived.
- **Qantas flight data link** (`RF`): the flight's number, route and scheduled time (`,FDASUB…,SYD,,DFW,,QF007,261025,0205,388,,VH-OQG`)
  appear in the submit, commit and status-request messages. Acknowledgements and replies carry numbers only, kept as sent.
- **Air Canada** (`4T`, `4Q`): `ATD`/`PSHBK` (left the gate), `TDX` (out and off), `ARV`/`TAR` (on and in), `KQ1` (all four times
  and the route), and `AGFSR`, an in-flight position report with altitude, phase, temperature and wind, drawn on the map as an
  "Airline" position. `4C` is the flight information sent before departure, `4M` the load sheet and `4P` the dangerous goods
  notice.
- **Ground events** (`49`, `17`): `01ICCL ACA025/311941CYVRZSPD` names an event, the flight, the time and the route. Air Canada,
  United, China Cargo and Zipair send them. The four-letter event codes are shown as sent; their meaning is not known.
- **787 engine and performance reports** (United and Air Canada, label `H1`): `/ILNGE7X.CLB006UA N249932UAL177 261024000658KDENLIRF…`,
  `APM … UAL182 KDENEDDF211026232013,` and `<701>IEG … ER …` are maintenance data, but each begins with the flight, the route and
  the whole date and time of the event, and most carry the aircraft's position. They are drawn as "Airline" positions, placed
  when they happened even if delivered days late, and name the flight at that time. A take-off report (`TOF`, or `TO` in the
  others) gives the moment of take-off to the second, which becomes the flight's departure time. The other numbers, and event
  codes beyond take-off, climb and cruise, are not interpreted.
- **787 position and takeoff reports** (read from Avianca's N781AV and checked against its own track; `EB` is also sent by United's N24993
  and Air Canada's C-FNOH). A summary starts with the airline only when the report names a flight to tell it by (`AVA4907`); `EB` and
  `EMR` name none, so they read "Airline position report" rather than guess:
  - `3N01 POSRPT 4907/LEMD/SKBO .N781AV/03A17:11 …` (label `83`, close kin of the Delta report below, with the time as `hh:mm` and no day):
    the flight and route, the position, the altitude **in feet**, speed, temperature, wind, fuel and the ETA. It is drawn as an
    "Airline" position.
  - `EMR05/06:31:44,N39537W005182,31541,0.804,…` (label `99`): the time to the second, the position, altitude, Mach number and true
    airspeed. The engine and system lines after it are not read.
  - `EB0032AA_ N781AV LEMDSKBO 36` with a line `40307-0050761720 24824-30…` (label `H1`): a position, the time, altitude, temperature
    and wind. `EB0031AA_` carries one a minute for several minutes: the newest is the position and the rest are listed in the
    message's text. A line cut short by the block size is read as far as it goes. The `EB0021CA_` report and the maintenance dumps
    stay undecoded.
  - `TKO01 AVA4907/28281720/LEMD/SKBO` (label `24`), the takeoff report: the time it left the gate (`DT`) and took off (`TO`), which
    become the flight's departure evidence like Air Canada's, and the fuel at takeoff in kg.
  - `PRG/DTLEMD,32L,186,070523,046/FNAVA182…` (label `H1`) is a progress report without a time of its own: the destination,
    runway, ETA and the flight. The numbers 186 and 046 are not understood.

- **Alaska Airlines** (read from N956AK and checked against its track). Label `80` movement messages: `1101 OFFRP 1388/31 KBUR/KSEA .N956AK`
  with `/OUT 1744/OFF 1804/…/ETA 2004` (left the gate, wheels up, ETA), `ONRP` (`/ON`), `INRANG` (a new ETA near the destination)
  and `FAROUT` (pushback). Door, cargo and acknowledgement messages on the label are left undecoded. On `H1`: the takeoff report
  (`N956AK 21388KBURKSEATO311026180429`, the takeoff to the second, which becomes the flight's departure), the performance report
  (`APM`, read as United's is, behind a line holding a message number) and the `ABS` position report, a cousin of `EB` whose fields
  are wider: latitude and longitude in thousandths of a degree, the time, a six digit altitude, the temperature and the wind. The
  flight is "AS" and its number; only the performance report spells the callsign. A later block of a long `ABS` message has no
  header and is not read. Positions agree with ADS-B to the minute.
- **EVA Air** (read from B-16783): labels `11` out, `12` off, `13` on, `14` in: `OFF01EVA619    /--310633KLAXRCTP` is wheels up on the 31st at
  06:33Z, Los Angeles to Taipei. An arrival names its airport twice. The aircraft condition (`ACM`) reports and the engine numbers are not read.
- **Weather observations** (`A3…` climb, `D3…` descent, `E3…` cruise, on `H1`): a few points a minute or less apart, each with
  latitude and longitude, altitude, temperature and wind, as in `A3M2312343KSEAKLAX…` followed by
  `N47335W1221840455P003097006G0009`. The header gives the airports and the day of the month; the time comes from the report (a
  climb's lines carry none, and the report's own time is within half a minute of its last point; the others carry it on the first
  line). The newest timed point is the position, so a report delivered days late lands on the day it was made. They name the route and are
  drawn as "Airline" positions with their wind. Where a report has no position at all, the day and time still count for the flight it
  belongs to. Read from 306 reports, 231 checked against ADS-B (all within a minute's flying).

  These times are placed on the most recent day that is not after the message arrived, so a position that arrives hours late
  (several did, on 28 October) lands where it was.
- **Pre-departure clearances** (`A3`, `A4`, Beijing and Shanghai): the runway, SID and squawk
  (`ACA030 CLRD TO CYVR OFF 36R VIA MUGL7X SQUAWK 0075`), and the status of a request.
- **Weather requests** (`5U`): Qantas asking for D-ATIS, TAF or METAR for a list of airports, and Fiji Airways and Delta's `WXRQ`.

A **D-ATIS reply** sent to the aircraft is decoded as a `weather_report`: "D-ATIS arrival information P for PHNL: 060°/8 kt, 10 SM,
FEW022 BKN030 OVC060, 26/22 °C, QNH 29.88". A METAR uplinked to the aircraft (label `3T`) reads the same way. The weather is not
drawn on the map, but D-ATIS requests and replies show the airports they name. A **data link disconnect** (`.DIS.`) is shown as
such, with the aircraft's registration; the two-character code in it is kept but not interpreted.

These formats are read from samples, not specifications. Stored messages show them after `fullflight -redecode`.

## Oceanic clearances

A request for an oceanic clearance (`/EGGX.OC1/RCL 030 UAL26-ATSIX/1316 M085F370`: the entry fix and time, Mach and level) and
the centre's reply (`CLA`: the destination, the track, the route from the entry fix, and the level and Mach to hold) are decoded
as `oceanic_clearance`. The cleared route is drawn as its own layer, "Clearances (oceanic, departure)" in the legend (dashed teal,
a dot on each fix, the track, level and entry in the hover). It is only the route over the ocean, so it is not treated as the
flight's plan, and a filed plan stays the route that is projected along. Fixes given by coordinates (`62N020W`) are placed as
written; named ones from what we have learned of them or the navigation database. Seen so far: one clearance and two requests, so
a form that does not fit is left undecoded. Free text from the crew that mentions a route (a `7B` message) is shown as text and
not plotted.

## Pre-departure clearances

Qantas sends each aircraft its departure clearance at the gate over the VHF data link (label RA, `.HDQOXQF ~1 PDC UPLINK`): the
day and time it was issued, the flight, type, origin and departure time, the destination, the runway and departure procedure with
its transition, the route to fly, the first altitude, the departure frequency, the transponder code and now and then a remark.
They are decoded as `departure_clearance`, and the aircraft's short reply (the header again, `/~1`) as a reply to it. The route is
airways and fixes joined by `DCT` and, over the ocean, latitudes and longitudes in degrees and minutes (`4219S14239E`); a lone `T`
ends it, and the routes seen stop well short of the destination (Sydney to Johannesburg stops at 59S 92E).

The route is drawn in the same legend layer as the oceanic clearance, in amber, from the origin airport along the fixes it names,
with the runway, procedure, climb, departure frequency, squawk and remark in the hover; nothing is drawn on to the destination.
It is not the flight's plan, and the plan stays the route that is projected along. A clearance counts from the day and time it was
issued, not from when a station delivered it (one reached us three days late). The navigation database is from 2013, so a newer
fix may be missing, or share its name with an older one far away: a named point that sends the route more than 300 nm out of its
way and back is left off and listed as not placed. Stored messages show it after `fullflight -redecode`. Read from six clearances.

## FMS plan reports

Boeing FMS units send their plan with a position report, in pages. The first page is the position report (`POSN28258W082126,RDSOX,230650,181,JFRYS,…`)
followed by the route (`/RI:DA:KMCO:AA:KSEA(16R):D:RDSOX1.JFRYS:F:JFRYS..PATOY.Q116.JAWJA..`, the filed plan's format with the arrival runway in
brackets), performance numbers (`/PR…`) and the forecast for each waypoint (`/FH`): name, ETA (hhmmss), speed, altitude, fuel, temperature, wind
and the leg to the next. Later pages carry the rest of the list, cut anywhere, and the last gives the arrival (`/DTKSEA,16R,79,042536,73`: airport, runway
and the ETA at the runway). Read from N956AK's reports (51 messages, three pages each) and the landing agrees with its track.

- The first page stays an FMS position report and now carries its plan: the summary adds `plan KMCO→KSEA runway 16R, 16 fixes`, the plan is drawn
  and the projection follows it as for a filed plan, and the route counts as a `flight plan` report.
- The waypoints in the next two hours also go on the report as fixes "on its route", with their ETAs, so selecting the report draws
  them beside the aircraft (a name far away is too easily another place of the same name).
- Later pages are `progress` messages ("FMS route forecast, page 2: SGF 00:54Z to HETHR 04:17Z"), with each waypoint's ETA and altitude in
  the text. The last also gives the destination, which counts as a route report (`FMS forecast`).

## Predicted winds

Airlines that plan with forecast winds (Air Canada, Qantas and others) exchange them over ACARS, decoded as `wind_forecast`
messages:

- the aircraft's request (`REQPWI/TS054236,301026/WQ340.360.380:RW26L.UQQ.ORNAI...`: the levels and fixes it wants);
- the table sent back (`PWI/WD400,ECOMI,278067,400M54.NATES,...`: for each level, each fix's wind direction and speed and the
  temperature, plus any climb and descent winds);
- the receipt (`RESPWI/AK` acknowledged, `AC` accepted, `RJ` rejected).

Uplinks arrive in blocks, each starting `- #M` and a character, that can cut a fix name in two; they are joined, and the last four
characters (a checksum) are dropped. The summary reads "Predicted winds FL400/380/360/340, 25 fixes ECOMI → N59000W170000", and
the message text lists every wind. The formats and the receipt codes are read from samples, not a specification. Winds are not
drawn on the map. The fix list is the route the airline expects, which helps when the flight plan itself was never sent.

## Flight plans

Many FMS units send the filed flight plan (`FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..N25000W170000..TUNTO..IGURU.Q13..`) and
progress reports (`PRG/TS195622,301026/DTVHHH,07R,...,064946/FNUPS58`). They are decoded as `flight plan` and `progress`
messages, and they name the flight, so messages that were unidentified now belong to it. A progress report whose own timestamp is
more than 10 minutes from its arrival is not used to name the flight.

These are usually sent over VHF ACARS with no hex address, which the by-address fetch never returns. So each watched aircraft is
also searched for by text, every 15 minutes: its registration (logons and performance reports name the flight), then
`FPN/FN<callsign>` for each callsign on its watches and in its recent messages. Only that aircraft's results are kept.

`GET /api/plan/{hex}` serves the plans, resolved to positions: coordinates as sent, other fixes from the navigation database (of
several places with a name, the one nearest the point before), the airports at the ends. Fixes the database does not have are
listed as not placed. Identical copies are reported once, preferring the most complete. The plan also gives the watch its origin
and destination (`flight plan` as the source) and the flight its from/to.

On the map the plan of the flight shown is drawn as a dashed line with a dot at each fix (hover for the name, the airway, when it
was filed); the legend switches it off. The projection follows it: an aircraft within 60 nm of the route and heading roughly along
it (70 degrees) is projected along the legs, turning where they turn, and stops at the destination; otherwise the straight-line
projection is used. The plan is as filed, not as flown.

The navigation database is from 2013, and a fix introduced since can share its name with an older one far away. A fix the plan gave
no coordinates for, placed from the database, that sends the route more than 300 nm out of its way and back is left out of the plan
and listed as not placed (B-LRM's plan from Madrid put SUDAR in Russia, 800 nm from its place between DARVA and OGOLI in Uzbekistan).
The position is also remembered as wrong (table `fix_suspect`, with why), and is left out of every later lookup of that name: other plans
and the waypoints named in reports. A position learned from the aircraft's own reports is never dropped or marked.

Which plan is the flight's: the newest that arrived from 12 hours before the flight began until it ended (plans go out at the gate),
and whose callsign is the flight's own number (UAL1887 and UA1887 are the same flight; leading zeros do not count). A plan filed for
another number is passed over however near its time, since an aircraft's next flight is often filed while it is still on the last:
UA1887, with no message of its own, was drawn along UAL599's KIAD to KDEN plan from the evening before. A flight with no number, or a
plan with no callsign, is matched on time alone.

## Weather radar

"Weather radar" in the map legend (first entry, off until you tick it, and remembered; its **Live | Past** switch chooses this live layer or the recorded one below; after a reload a ticked row is back on Live, since the flight and time it was showing are not kept) lays [RainViewer](https://www.rainviewer.com)'s
global radar composite over the map, in the NEXRAD colours (green to red), at 65% opacity. A label under the clock gives the time
of the frame and its age; the newest frame is fetched when it is switched on and every 5 minutes after. The browser fetches it
straight from RainViewer (the server is not involved), and the map credits it; see RainViewer's terms for what it may be used
for. There is no radar over the open ocean, so oceanic routes show nothing there. Its tiles are made to zoom 7 and enlarged when
you zoom in further.

### Satellite (IR)

Over most of the oceans there is no ground radar, but storms still show in the infrared: the colder the cloud top, the taller the
cloud. "Satellite (IR)" in the legend (also remembered) lays NASA [GIBS](https://earthdata.nasa.gov/gibs)'s band 13 "clean
infrared" imagery over the map at 55% opacity. In GIBS's tiles only cold, high cloud tops are coloured (blue to red); everything else, warm cloud, land and sea, is a grey ramp. The page makes the grey transparent (a pixel whose red, green and blue are within 6 of each other), so only the cloud tops are drawn and the map is not darkened; in the tiles tried that cleared 86 to 92 percent of the pixels. GIBS allows its pictures to be read from another site, which is what lets the page do this; a picture that cannot be read back is drawn as it came. It comes
from the three geostationary satellites that cover the Pacific and the Americas: Himawari (60E to the date line), GOES-West (to
105W) and GOES-East (to 6E). Each draws only the longitudes it sees well, since imagery near a satellite's edge is stretched into
streaks. **Europe, Africa and the western Indian Ocean are not covered.**

GIBS serves frames 10 minutes apart and the newest is about half an hour old; the newest of each satellite is found by trying back
from now, and found again every 10 minutes. Frames have holes, so a missing tile is taken from the previous frame or the one
before that. A label under the clock says which frames are showing and how old the oldest is. Infrared sees cloud tops, not rain:
cold high cirrus can look like a storm and heavy rain from low cloud can be missed. Faint lines between tiles are in the
imagery, not a gap.

### Satellite IR at the selected time

The **Past** side of the legend's "Satellite (IR)" row draws the frame for the time of the selected message (or of the newest
position when none is selected) instead of the newest. Nothing is recorded for it: GIBS keeps its infrared frames for about 90
days (a frame of 2026-08-06 was there on 2026-11-03 and one of 2026-08-03 was not), so the page asks GIBS for the old frame
directly, as the live layer does for the new one, with the same satellites, hole filling and 55% opacity. The frame is the
ten-minute one at or before the time, and never one of the newest half hour, which is not yet there. A badge says which frame,
with the date when it is not today's, and how far it is from the time wanted, the same way as the radar badge (`+10m`, `-10m`); its arrows step ten minutes, and clicking an ADS-B dot sets the time to that dot's (see the time slider below). The radar
layer follows the same time, so stepping either moves both. For a time older than the archive (88 days, a little inside what was
found) it says so and draws nothing. Frames older than that are lost unless they were kept, which is what the plan's filtered
copy would do ([the plan](weather-recording-plan.md)).

## Estimated path and ADS-B points

Where the aircraft went quiet and was next heard from, a dotted grey line joins the two fixes: ADS-C handing over to ADS-B, or a
hole in ADS-B coverage. It follows the filed route when the plan covers both fixes (bending at its waypoints), else it is a
straight line; hover for the two fixes, the time and the distance. It is drawn only for a silence of 5 minutes to 3 hours between
trustworthy fixes of one flight, never across an impossible speed, and not where the two fixes are already one continuous track
(in the unified view, within the sparser source's usual interval; by source, the same source within its own). It has its own
legend entry ("Estimated path").

The ADS-B track has a small hoverable dot every 2.5 minutes and 6 nm or so (a point is kept only if it is at least that far in
time and distance from the last one kept), showing the callsign, time, altitude, speed and track. They are on the ADS-B layer.

## Ghost position for a message with no position

Select a message that carries no position and, if trustworthy fixes (ADS-B or datalink) lie either side of its time, a faded
plane appears where the flight path puts it, interpolated in time along the line between them; hover for which two fixes it used.
It shows only for the selected message. There is none beyond the last fix (the projection covers that), across a silence of more
than 3 hours, between two different flights, or between fixes no aircraft could fly between.

## Projection

An estimated position now (a grey ghost plane) and a dotted look-ahead are dead-reckoned from the newest trustworthy fix along its
track at its ground speed (or the speed and track worked out from the last two fixes). The ghost appears only once the newest fix
is 3 minutes old; a fresher one (ADS-B arrives about every 45 s) is the plane's position, and only the look-ahead is drawn from
it. Limits: never from a fix over an hour old, never more than 90 minutes beyond the fix, not for an aircraft on the ground or
outside 100-700 kt, and it stops at a known destination (the one you set, else a recent REP081 one) rather than flying past it.
The look-ahead past now (off, 15, 30 or 60 min) is the selector under the watch list. It does not know about turns or weather
deviations.

When the flight's filed plan is known and the aircraft is near it, the projection follows the plan's legs but starts from where
the aircraft *is*: the distance from the line at the last fix is kept and closed over 150 nm (or by the end of the route), so the
dotted path never begins on the line when the aircraft is beside it. Where the plan's own line is poor (a fix missing from the
2013 database leaves a long straight leg), the route ahead is shaped by the next and next-but-one waypoints of the newest ADS-C
report (under 45 minutes old, and still ahead of the aircraft), which are exact, and then rejoins the plan. A fix name's position
is taken from what we have learned from the aircraft's own reports (`nav db (learned)` in the plan's hover) before the navigation
database.

### How the plane shows how stale its fix is

The plane changes in steps that follow what the projection does with its fix, with a badge giving the age (`7m`, `2.5h`) that
keeps counting between refreshes. Its hover says which step it is in. (Whether the look-ahead is switched off is a separate
setting.)

| age of the newest fix | plane | meaning |
|---|---|---|
| under 3 min | red | live; no ghost needed |
| 3 min | red, age badge | the estimated-position ghost is drawn ahead of it |
| 15 min | orange-red | the estimate is getting less certain |
| 30 min | amber-brown | the estimate is a long extrapolation |
| 60 min | grey, dashed ring | too old to project from: no ghost, no look-ahead |

### Landing

Many arrivals drop out of ADS-B coverage on the final approach or the runway, and dead reckoning from such a fix would fly the
aircraft on past the runway. So the server infers a landing, with or without a known destination, when:

- the newest position the aircraft has, from any source, is airborne and under 5,000 ft and 250 kt, or on the ground;
- an airport (large, medium or with an IATA code) is within 15 nm and, if the track is known, within 60 degrees of it (a known
  destination wins when several are near); and
- the time to reach it at the fix's speed, plus a minute for a late fix, has passed.

The watch then carries `landing` (airport, estimated touchdown, the evidence); the plane turns green (with no badge: its tooltip says whether it landed or is the next flight waiting), the
projection is not drawn, and the sidebar and hover say `landed RJAA ~03:26:44Z`. A newer position of any kind clears it. It is an
inference: a missed approach that then leaves coverage would be called a landing until the aircraft is heard again.

An aircraft seen on the ground is landed from when its stay began (its first position on the ground after the last in the air),
not from its newest fix. Once ADS-B gives it a different callsign than the flight it flew in on, the sidebar and hover say
`UAL177 not departed, at KDEN since 20:53Z`: it is the next flight, waiting at the gate.

## Map tiles

The map pages ask this server for the street map (`/tiles/osm/{z}/{x}/{y}.png`) and never OpenStreetMap directly. With
`map_tiles.cache` off (the default) the server answers each request with a redirect to `tile.openstreetmap.org`, so the browser
fetches the tile from OSM itself, as it always did. With it on, the tile is kept under `map_tiles.dir` and served from there:
several browsers on one server cost OSM one request per tile. Radar and infrared tiles are not cached; they are immutable and
the browser already keeps them.

OSM says it does not recommend a caching proxy in front of its tile server, and allows one on conditions, which this meets:

- Every request names FullFlight in its User-Agent (with its web address) and carries the project's address as its Referer: a
  server has no page of its own to name.
- A tile is kept as long as OSM's own `Cache-Control` or `Expires` says (seven days when it says nothing, a day to 30 days at
  the extremes), and once it has expired it is asked about again with `If-None-Match` and `If-Modified-Since`.
- Nothing is fetched that nobody asked for: no prefetching, no scan of a wider area. A tile that cannot exist (outside the zoom
  range or the grid) is refused without asking OSM.
- A tile is fetched once however many browsers ask for it at that moment, and never more than two at a time. A 429, a 403 or
  a server error from OSM stops the asking for ten minutes; tiles already held (even expired ones) are still served.

The directory is kept under `map_tiles.max_mib` (default 500) by deleting the oldest tiles. Browsers keep a tile for a day.

## Web page

The sidebar's Watches tab is split in two, each part folding away like an accordion: **Targets** above (the add form stays put,
the list of watches scrolls on its own) and **Messages** below (with the flight selector and the projection control above its own
scrolling list). Fold one and the other takes the height; the header of a folded Targets shows the selected target, and each
header counts what is in it. Your choice is remembered, and choosing a message on the map opens Messages if it is folded.

**The time slider.** With one flight shown (not "all flights") and at least two positions on it, a slider along the bottom of the map
spans the flight from its first position to its last. It sets the same time as selecting a message, clicking an ADS-B dot or the
badge arrows, so the recorded radar and the infrared follow it, and a translucent blue aircraft shows where the aircraft was then:
at the dot or report itself, between the two positions either side by interpolation (altitude and speed too), or, where the track
has a silence, along the dotted estimated path, with the tooltip saying it is an estimate. The slider's handle is a small rectangle with the time of day (`16:25Z`; the dates are at the ends of the bar) written in it, and all the way to the right it says **Live**: the aircraft is
not marked, and the radar and the satellite are set to **Live**. Moving it left chooses a time. (Dragging to the right end again,
or choosing another target or flight, goes back to Live.) The right end is the newest position, and while live it ticks up as
positions arrive. With the slider focused (click it, or Tab to it) the arrow keys move the handle a minute, Shift ten minutes, Page Up and Down ten minutes, and Home and End go to the first position and to Live. The live end is a detent for the pointer. The handle follows the pointer all the way and never jumps: as soon as it is within 6 px of the right-hand end (a short blue stretch on the track marks the zone) it turns blue and reads Live, and the weather and the aircraft go live with it, and it settles at the end when the pointer is let go; once in the zone it takes 10 px to pull it away, so a shaky pointer does not flicker. At a chosen time the handle is a control grey, and blue when live, like the active side of the legend's Live | Past switch. The keyboard is exact, so one press leaves Live. The page refreshes every 30 seconds and re-applies the selected message, which does not take a live slider back to that message's time; picking a message again does. The handle does not jump to the pointer when it is pressed: it moves only as the pointer does. (A press on the bare track still moves it there.) The bar keeps one size, and the times at its ends take only the room they need. The start carries its date when it is not today's, the end is a time of day, and when the flight crosses midnight UTC the end is marked `+1` (`00:20Z+1`), the aviation way, in room kept for it so it does not move the slider. The handle shows only the time of day: where it sits on the track says which day. Going the other way, the first time chosen after that (the slider, a
message, a dot or an arrow) sets both to **Past**. Each of the two rows in the legend, "Weather radar" and "Satellite (IR)", has a
**Live | Past** switch; it is the viewer's after that, so moving the time does not undo it, and it keeps its setting while the row is
unticked, for when it is ticked. Only one of the two versions of a row is ever on the map. There are no message ticks on it (the messages are on
the track), and no play button yet.

**Folding the legend.** A **Legend ▾** bar above the two legend boxes at the bottom left folds them away (the bar then reads
**Legend ▸**) and brings them back. It only hides the checkboxes: layers that are ticked stay on and the track keeps drawing. The
choice is remembered, and the legend starts shown.

Choosing a watch centres the map (zoom 7) on that aircraft's newest trustworthy fix, from ADS-B or a datalink message, whichever
is later.

**The unified track.** By default one track runs through every plausible position, ADS-B and datalink reports together, in time
order. Each source keeps its colour (ADS-B blue, ADS-C orange, CPDLC purple, REP081 teal, FMS green, ACMF crimson): where the
source changes, the join takes the first source's colour up to its midpoint and the next one's from there. Positions flagged
impossible or with an unverified time are left out, as is a lone fix that no neighbour agrees with, and a report with ADS-B
within 3 minutes on both sides keeps its dot but not its line (ADS-B is denser and more exact). A silence longer than the sparser
of the two sources allows (ADS-B 20 min, ADS-C 90, CPDLC 150, REP081 90, FMS 60, ACMF 40) or an impossible jump (over 700 kt)
ends the solid line, and the dotted grey estimate takes over across a silence.

**The legend** shows each source as its dot with a line out of each side, and has two radio buttons: **Unified track** or **Track
by source** (a line per source in its own dash style, broken at that source's own silences). The choice is remembered. A source's
checkbox shows or hides its dots and, in the unified view, takes its fixes out of the line too.

**Dots, messages and the plane.** Click a dot on the map and its message is selected in the list (and scrolled to); click a
message that has a position (orange bar on its left) to ring it on the map and show its popup (only one popup is ever open); the
map pans only if it is out of view. Position messages are listed at the time the report carries, with a dotted underline, and the
list is ordered by the time shown; hover for both times and the gap between them. Other messages show when they arrived. Hover
any message time for its age. The aircraft is drawn as a plane turned to its track; hovering it shows altitude, climb/descent
trend, ground speed and track.

Everything drawn shares one longitude frame, anchored at the chosen aircraft's newest fix, so a track crossing the date line
continues from 179 to 181 instead of drawing a line round the world.

`node scripts/uitest.js` and the other scripts in `scripts/` (they need node) drive the page scripts against a fake DOM.
