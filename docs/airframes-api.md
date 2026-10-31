# Spike findings (2026-10-30)

Probed with plain `curl`, no API key, from the dev machine.

## ADS-B sources

| Source | Result |
|---|---|
| `api.airplanes.live/v2/*` | **HTTP 403** for every request, with or without a custom User-Agent: "Please contact contact@airplanes.live. Your email MUST include any links, a description of the project…". Access is now gated; HANDOFF's assumption of an open API is out of date. |
| `api.adsb.lol/v2/*` | Works, no key. Same ADSBx-v2 JSON (`ac[]`, `hex`, `flight`, `r`, `t`, `lat`, `lon`, `alt_baro`, `gs`, `track`, `seen_pos`). |
| `opendata.adsb.fi/api/v2/*` | Works, no key. Same schema. |

Working endpoints (adsb.lol and adsb.fi, both verified against a live aircraft):

- `/v2/hex/{hex}`
- `/v2/callsign/{callsign}` (ICAO callsign, e.g. `SWA1674`, not IATA `WN1674`)
- `/v2/reg/{tail}` on adsb.lol; `/v2/registration/{tail}` on adsb.fi
- `/v2/point/{lat}/{lon}/{radius_nm}`

Not found in the response: `"ac": []`, `"total": 0` (HTTP 200). Treat as "not airborne / no coverage".
Sample: `testdata/adsblol_point_sample.json`.

Decision: make the ADS-B provider pluggable (base URL + reg path in config). Default adsb.lol, with adsb.fi as fallback. Add airplanes.live if its operators grant access.

### airplanes.live, as of 2026-10-31

Its API reference (pasted from the site) documents `GET https://api.airplanes.live/v2/reg/{reg}` (comma-separated
registrations allowed) returning the same `{ac:[...], msg, now, total, ctime, ptime}` envelope, and by the same pattern
`/v2/hex/{hex}`, `/v2/callsign/{cs}` and `/v2/point/{lat}/{lon}/{nm}`. The reference says nothing about keys or approval,
but unauthenticated requests from here still got **HTTP 403** ("Please contact us at contact@airplanes.live. Your email
MUST include any links, a description of the project…"), so it stays opt-in: name `airplanes.live` in `adsb.sources`
once its operators have agreed. A source that answers 401/403 is left alone for an hour, 429 for 30 seconds (or its
`Retry-After`), and the pause is logged once with the HTTP status.

Fields it documents that the other two may not send, and we do not read yet: `wd`/`ws` (wind direction and speed,
calculated), `oat`/`tat` (outside and total air temperature, calculated, not below Mach 0.395), `tas`/`ias`/`mach`,
`nav_altitude_mcp`/`nav_modes`/`nav_qnh` (what the autopilot is set to), `lastPosition` (when the live position is
older than 60 s), `rr_lat`/`rr_lon` (a rough position when there is no ADS-B or MLAT one), `dbFlags` (military, etc).
Wind and temperature at every ADS-B point would fill the hover the way they do for datalink reports.

## airframes.io (`https://api.airframes.io/v1`), works unauthenticated

- `GET /airframes/icao/{hex}` and `GET /airframes/tail/{tail}` → `{id, tail, icao, icaoType, …}`. (`/airframes/hex/…` is 404.)
- `GET /messages?icao={hex}&limit=N` → **filters correctly** (verified: all rows are B-LRM). Newest first.
- `GET /messages?tail=…` and `?airframe_id=…` are **silently ignored** and return the global firehose. Don't use them.
- `GET /flights?flight=UA869` is also ignored (returned unrelated flights). Flight lookup by callsign must come from ADS-B, or from filtering `/flights` client-side.
- Message object: `id`, `timestamp`, `airframe{}`, `flight{}` (airframes' derived hints), `source`, `sourceType` (vdl/hfdl/…), `fromHex`, `toHex`, `label`, `blockId`, `messageNumber`, `text`, `data`, `frequency`, `level`, and a nested `station{}` that includes the station owner's user record. Drop `station.user` on ingest.
- Rate-limit headers are exposed (`X-RateLimit-*`, `Retry-After`) but are not shown on the 404s I saw. Check on a 200 with a larger sample.

Still to confirm: pagination params (`before`/`since`/`until`/`page`), the max `limit`, and the real rate limit.

## Watchlist design (adds to HANDOFF goal 1)

Entries are **time-windowed**, so a flight can be watched before it departs.

| Kind | Input | Example | Meaning |
|---|---|---|---|
| `flight` | flight number + date | `UA869 2026-10-31` | Poll for the ICAO callsign (`UAL869`) from the window start. Once an aircraft is seen with that callsign, **latch its hex** onto the entry and follow it by hex (callsigns can change; the hex doesn't). |
| `tail` | tail or hex | `B-LRM` | Track immediately. |

Common fields: `start`, `end`, `status` (`waiting` → `tracking` → `done` | `expired`).

- **Flight window:** the date is the scheduled departure date. It is ambiguous across time zones, so interpret it as UTC with a ±1 day tolerance, or let the user give an optional time/zone. Default `start` = date 00:00 UTC − 12 h, `end` = start + 60 h (12 h lead, a day of date slop, then 36 h of flight). Stop at first landing/disappearance after being latched.
- **Tail window:** `start` = now, default `end` = now + 36 h (longest scheduled flights are ~19 h today, Project Sunrise ~22 h), editable at add time ("until" date or "forever").
- **Callsign mapping:** the user types the IATA number (UA869); ADS-B uses the ICAO callsign (UAL869). Needs an airline-designator table (IATA→ICAO), plus accept a raw ICAO callsign. Beware that callsigns sometimes differ from the flight number (e.g. `CPA833` vs `CX833`, leading zeros, `UAL869` vs `UAL0869`), so try several variants.
- **Cost:** a waiting flight entry costs one `/callsign` request per poll. Poll waiting entries slowly (every 2–5 min), tracked ones every 30–60 s. Batch where possible, and respect the upstream 1 req/s etiquette.
- **Expiry:** a sweeper marks entries `done` or `expired` and stops polling.
