# Datalink Tracker — Project Handoff

Working name. A small self-hosted web app that follows specific aircraft by
combining **live ADS-B positions** (airplanes.live) with **datalink messages**
(airframes.io: ACARS / ADS-C / CPDLC / AFN / MIAM), decoded and shown on one
page: a map with the track, and a timeline of decoded messages.

Inspiration: we manually followed Cathay Pacific A350s B-LRM (hex `780AB8`)
and B-LRD (hex `780AA4`) across the Pacific by pasting airframes.io messages
into a decoder and comparing against FlightAware. This app automates that.

---

## Goals (v1)

1. **Watchlist** of aircraft, keyed by **ICAO hex** (and tail number).
2. **Position poller**: periodically fetch each watched hex from airplanes.live
   and store a track log.
3. **Message fetcher**: pull datalink messages for each watched aircraft from
   airframes.io, store raw text plus metadata.
4. **Decoder**: decode ADS-C, CPDLC, AFN, and MIAM messages with **libacars**,
   and store structured results (positions, waypoints, ETAs, WILCOs, etc.).
5. **Web page** per aircraft:
   - Map: ADS-B track (line) + ADS-C / message-derived positions (markers,
     different color) + predicted waypoints from ADS-C.
   - Timeline: messages in time order, raw text plus a human-readable decode.
   - Flight segmentation: group by flight number **and date** (see gotchas).
6. **Auth**: not public. See "Access & legal" below.

## Non-goals (v1)

- Global coverage or showing every aircraft. Watchlist only.
- Running our own SDR receivers (maybe later; see "Future").
- Commercial use (airplanes.live API is non-commercial).

---

## Tech choices

- **Language: Go** (owner already writes Go). Single binary, plus static frontend.
- **Storage: SQLite** (e.g. `modernc.org/sqlite` to avoid cgo, unless cgo is
  needed anyway for libacars).
- **Frontend:** plain HTML/JS + **Leaflet** with OpenStreetMap tiles. No heavy
  framework needed.
- **Deploy target:** a Debian box at home, systemd service, behind a reverse
  proxy. Docker optional.
- **Config:** a single YAML or TOML file: watchlist, API keys, poll intervals,
  listen address, DB path.

### libacars integration

> **Update:** the decoder now uses **golibacars** (https://github.com/avanha/golibacars), a pure-Go port of libacars
> (CPDLC's ASN.1 PER included) that is checked against libacars byte for byte. Options 1 to 3 below, and the
> statement that CPDLC is not worth porting, describe the original plan and no longer apply; there is no cgo.

libacars (https://github.com/szpajder/libacars) is C. Options, in order of
preference (pick after a quick spike):

1. **cgo bindings** to `la_acars_decode_apps()` / the JSON output functions.
   libacars can emit JSON, which is the cleanest thing to consume.
2. **Shell out** to its example CLI `decode_acars_apps <u|d> <label> '<text>'`
   (built in `build/examples/`). Simple but text output needs parsing.
3. Port only the ADS-C decoder to Go (ADS-C is straightforward bit packing).
   CPDLC is ASN.1 PER and is **not** worth porting. Keep libacars for it.

---

## Data sources

### airplanes.live (ADS-B, live only)

Docs: https://airplanes.live/api-guide

- Base: `http://api.airplanes.live/v2/`
- `GET /hex/{hex}`, `GET /callsign/{callsign}`, `GET /point/{lat}/{lon}/{radius_nm}` (radius up to 250 nm)
- **Rate limit: 1 request/second.** Non-commercial. No SLA.
- **No documented history endpoint.** We build our own track log by polling.
- Response is ADSBExchange-v2-style JSON (`ac[]` with `hex`, `flight`, `r`
  (registration), `t` (type), `lat`, `lon`, `alt_baro`, `gs`, `track`,
  `seen`, `seen_pos`, …). **Verify field names against a live response.**

Polling plan:
- One server-side poller for the whole watchlist. **Never** proxy user
  requests directly to the API.
- Default interval 30–60 s per aircraft; space requests to stay well under 1 req/s.
- Store a point only when `seen_pos` is fresh (e.g. < 60 s) and the position changed.
- If the aircraft isn't in the response, it's out of ADS-B coverage. Store nothing.

### airframes.io (datalink messages, has history)

Docs: https://docs.airframes.io/api and https://docs.airframes.io/api/authentication

- Base: `https://api.airframes.io/v1`
- Resources: flights ("live and historical… with events, messages and
  position trails"), messages, airframes (lookup by tail, ICAO hex, etc.).
- Time filters: `since` / `until` (ISO-8601).
- Auth: docs are inconsistent ("all requests require an API key" vs "optional
  on public data endpoints"). A key (`Authorization: Bearer …` or `X-API-KEY`)
  raises rate limits. Feeders get a free account.
- Rate-limit headers: `X-RateLimit-Limit/Remaining/Reset`; `429` + `Retry-After`.
- **Unverified:** a community post reported a 404 on `/v1`. **First task:
  spike the API, confirm working endpoints and response schemas, and record
  them in `docs/airframes-api.md`.**

Fetch plan:
- Resolve each watched tail/hex to an airframes airframe id once, then cache it.
- Every few minutes, fetch messages for that airframe `since` the last fetch.
- Backfill on add: fetch the last ~7 days.
- Deduplicate (the same message can be received by several stations).

---

## Principle: do our own decoding, treat airframes.io metadata as hints

airframes.io's derived fields are **not reliable**. We saw a message whose raw
text said callsign `CPA833` / tail `B-LRD` while airframes.io tagged it as
flight `CX852`, plus station timestamps that were minutes or days off.

- Store the **raw message text** and the raw ACARS fields (label, block ID,
  message number, direction, station, receive times) exactly as received.
- Derive identity **ourselves from the raw text**: callsign from the
  `/FMH<callsign>,` part of AFN messages, the 6-char flight ID in the 10-char
  prefix (`J27ACX0833` → `CX0833`), the tail from `..B-LRD` in ADS-C/CPDLC
  addresses, and the tail/flight/route inside MIAM XML or REP081 bodies.
- Derive times from embedded timestamps where present (see below).
- Keep airframes.io's own flight/tail/time fields in separate `af_*` columns.
  When they disagree with our decode, **our decode wins**. Log the mismatch
  and show a small warning badge in the UI.
- All decoding goes through our decoder package (libacars + small hand
  parsers), never through airframes.io's decoded or "formatted" output.

## Decoding notes & gotchas (learned the hard way)

These are the most important part of this doc. Build tests around them.

### Message text prefix

airframes.io message text for satcom/ACARS often starts with a **10-char
prefix**: 4-char message number + 6-char flight ID, e.g. `J27ACX0833` or
`U99ACX0111`. libacars wants the application text **without** it:

- ADS-C / CPDLC / AFN: strip everything before the first `/`
  (`J25ACX0833/FUKJJYA.ADS..B-LRM07…` → `/FUKJJYA.ADS..B-LRM07…`).
- MIAM: strip the prefix so text starts at `T0…`/`T-…`
  (`U99ACX0111T-2!<<…` → `T-2!<<…`).
- Some VDL2 messages have no prefix (`/OAKODYA.ADS..B-LRM03…`).

### Label & direction

libacars needs the ACARS label and direction. Observed mapping:

| App | Example address | Label | Direction |
|---|---|---|---|
| ADS-C | `/FUKJJYA.ADS..` | `B6` | downlink |
| CPDLC | `/BNECAYA.AT1..` | `BA` | downlink |
| AFN logon/contact | `/USADCXA.AFN/…` | `B0` (down), `A0` (up) | |
| D-ATIS request | `/VHHH.TI2/…` | `B9` | downlink |
| MIAM | `T0…` / `T-2!…` | `MA` | downlink |
| Media advisory | `0ES…`, `0LV…` | `SA` | downlink |

Rule of thumb: labels `A*` = uplink, `B*` = downlink. With the wrong label,
libacars may silently decode nothing.

### Timestamps (critical)

- **ADS-C times are seconds past the hour only.** Resolve the hour from the
  receive time: choose the hour that puts the report at or just before the
  receive time (within ~60 min).
- **Station clocks lie.** On `TBG-CBAND-APAC`, airframes "Message time" ran
  ~3m20s **behind** the time embedded in messages; "Archived" was within a few
  seconds. Prefer embedded times, then Archived, then Message time.
- **Backfilled stations:** `EB-LZIB` (VDL, Slovakia) uploaded a backlog with
  Message time == Archived == upload time, but the MIAM body was dated **two
  days earlier**. Hour resolution for ADS-C from such a station would be wrong.
  Mitigation: when a message carries a full date (MIAM XML, REP081), trust it;
  flag ADS-C positions that are wildly inconsistent with the ADS-B track or
  with the aircraft's other reports (e.g. implied speed > 700 kt).
- Store everything in **UTC**.

### Flight identity

- **Match aircraft by hex/tail, never by callsign alone.** airframes.io
  mislabeled at least one message's flight (raw text said `CPA833`, tagged as
  `CX852`).
- **Same flight number repeats daily on different aircraft.** We wasted a night
  comparing Oct 29's CX833 (B-LRD) with Oct 28's CX833 (B-LRM). Segment flights
  by (tail, flight number, departure date), and always show dates in the UI.
- Hex ↔ tail is fixed per airframe (`780AB8` = B-LRM, `780AA4` = B-LRD).
  Registration country from the hex block may read "China" for Hong Kong (B-L…)
  aircraft. That's expected.

### Cathay A350 `REP081` report (MIAM body, partially decoded)

Example (B-LRD, CX111, VHHH→YSSY):

```
…|A350,000279,1,1,TB000000/REP081,01,01;A06/NX,VHHH YSSY/0,7,-060908,+512958,2482551,3900,-527,237,039,X,3,5,0,0,51,48,73,72,45/:
```

Fields after `/0,7,`, validated on 2 samples against simultaneous ADS-C:

| Field | Example | Meaning | Confidence |
|---|---|---|---|
| 1 | `-060908` | latitude, arcseconds (÷3600 → −16.919°) | high |
| 2 | `+512958` | longitude, arcseconds (÷3600 → 142.488°) | high |
| 3 | `2482551` | seconds since 00:00 UTC on the 1st of the month (→ Oct 29 17:35:51) | high |
| 4 | `3900` | altitude in tens of feet (FL390) | high |
| 5 | `-527` | outside air temp, tenths °C (−52.7) | high |
| 6–7 | `237,039` | wind direction °, speed kt | high |
| rest | `X,3,5,0,0,51,48,73,72,45` | unknown (engine/system data?) | — |

`VHHH YSSY` = origin/destination ICAO. `000279` looks like a running counter.

---

## Test fixtures (real messages with libacars output)

Put these in `testdata/` and assert the decoded values.

1. **ADS-C periodic report** (B-LRM CX833, archived 2026-10-28 04:07:17 UTC), label B6, downlink:
   `/FUKJJYA.ADS..B-LRM07139FFB6420C94706B41F1604805B1928D888A01600ED5C613878A4A01712DD1B45BA49C3C8AC0E5B18EC40000F5B29A8C0000D13333355554946C44011C71B1C71C9C3C076C4`
   → lat 27.5974846, lon 152.6006126, alt 38000, time :07:09 (→ 04:07:09Z),
   GS 472.5 kt, track 256.2°, Mach 0.8495; next WP 27.0/150.0 ETA +1088 s, then 25.0/140.0.

2. **ADS-C waypoint change** (B-LRM), label B6, downlink:
   `/FUKJJYA.ADS..B-LRM141332A3553B094717F01F0D11C71B1C71C946D022105BD2DE544946C02882`
   → 26.9968414, 149.9819183, 38000 ft, :25:32; next 25.0/140.0 ETA +4130 s, then 23.0043/129.0813.

3. **ADS-C at takeoff, SEA** (B-LRM, VDL station KK7JPR-BAINBRIDGE), label B6, downlink:
   `/OAKODYA.ADS..B-LRM03000721B84D4834C074C8E81F0E40105102A40F41B08802A40D21B4354833C0A4401B21A66D482F00D8805E4B`
   → ack contract 0; 47.4185371, −122.3075294, 1868 ft, :09:30; track 180.2°, GS 162 kt, VS +2704 fpm.
   Note: embedded time 19:09:30Z but both receiving stations logged 19:22Z.

4. **ADS-C with meteo** (B-LRD CX111 to Brisbane), label B6, downlink:
   `/BNECAYA.ADS..B-LRD07F4A0DB27E24985DA6C1F0E36A0F180001022A9FE5C0DF35D532D1D49854371F161AB35924985409F6B`
   → −15.9914589, 142.0108223, 39004 ft, :28:11; wind 235.5°/34.5 kt, temp −52.5 °C.

5. **CPDLC position report** (B-LRM CX143 to Manila), label BA, downlink:
   `/RPHIAYA.AT1..B-LRM20C0654C3D9F3B9319D7382D01B2A8253830A74F80E2448332941B424645196C5B015BD622559D52ACC7F59390DF83`
   → 19°10.3'N 115°52.3'E, FL370, 16:06Z, next SABNO 16:07, then DALRA,
   last UNTUL 15:58, M0.85, GS 520 kt.

6. **CPDLC WILCO** (B-LRD CX110 to Makassar), label BA, downlink:
   `/UPGCAYA.AT1..B-LRD628C409D00341F` → WILCO, msg ref 6, 04:02:29.

7. **MIAM maintenance report** (B-LRD, station EB-LZIB), label MA, downlink:
   text begins `T02!<<.p/hTIB9N"8J!'s.16Uj(G*jktJ|Xcc=…` (full string in the
   original airframes.io message). libacars inflates it to XML: tail B-LRD,
   A350-900 MSN 0038, flight CPA301 VHHH→EDDM, dated 2026/10/27 04:53:20, fault
   `SURV SYS 2 FAULT` (ATA 3471) then `MANUAL RESET`. Fetch the full text from
   airframes.io for the fixture.

8. **AFN contact advisory** (uplink, B-LRD), label A0:
   `/YVRE2YA.AFN/FMHCPA833,..B-LRD,,191904/FCAOAKODYA,02B37`
   (instructs logon to OAKODYA). libacars support for AFN may be limited; a
   small hand parser is fine.

9. **Media advisory**, label SA: `0ES124056VS` = satcom link Established at
   12:40:56; `0LV193609S` = VHF link Lost at 19:36:09. Hand-parse.

10. **D-ATIS request**, label B9: `/VHHH.TI2/000VHHHA85D0` = arrival ATIS
    request for VHHH (`A` = arrival). Hand-parse.

---

## Suggested milestones

1. **Spike:** hit both APIs, save sample responses to `testdata/`, write
   `docs/airframes-api.md` with confirmed endpoints. Build libacars and pick
   the integration approach.
2. **Decoder package** with the fixtures above passing.
3. **Storage + pollers** (airplanes.live track, airframes messages), config
   file, watchlist.
4. **Web UI:** aircraft list → aircraft page (map + timeline), flight segmentation.
5. **Auth** and deploy (systemd unit, reverse proxy example).
6. Nice-to-haves: compare ADS-C predicted ETAs to the actual waypoint passage
   (from the next ADS-C waypoint event or the ADS-B track) and show the error;
   live updates via SSE.

---

## Access & legal

- Keep the **code** public if desired. Commit **no** API keys or DB files
  (`.gitignore` the config and data dir; ship `config.example.yaml`).
- Keep the **running site** private: invite-only accounts, or behind the home
  reverse proxy / VPN. No anonymous access.
- Respect upstream terms: airplanes.live is **non-commercial** and **1 req/s**;
  read airframes.io's terms on redistribution before exposing messages to
  other users.
- Not legal advice: in the US, publishing the contents of radio communications
  not intended for the public can raise issues (Communications Act §705).
  Another reason to keep it private and small.

## Future

- Own receivers: readsb (1090 MHz ADS-B), dumpvdl2 (VDL2 ~136 MHz), dumphfdl
  (HFDL) feeding this app directly, plus feeding airframes.io / airplanes.live.
- ACARS Hub (sdr-enthusiasts) as a reference for UI ideas.
