# Recording the weather along a flight: a plan

Status: a proposal, nothing built. Written 2026-11-03.

## Goal

Show what the weather looked like when an aircraft was there, so a course deviation can be compared with the radar and
cloud tops that were around it at the time. The live map already draws RainViewer radar and NASA GIBS infrared, but only
as they are now; once the moment passes, it is gone.

## What the sources keep (checked 2026-11-03)

| Source | History available | Frame spacing | Tile size (z6) |
|---|---|---|---|
| RainViewer radar | **2 hours** (13 frames, 120 minutes exactly) | 10 min | not measured; mostly empty tiles are tiny |
| NASA GIBS band 13 IR (Himawari, GOES-East, GOES-West) | **about 90 days** (worked at 2026-08-06, 404 at 2026-08-03) | 10 min | 18 to 39 KB, about 27 KB on average (six Himawari tiles) |

- RainViewer's index lists no nowcast and no satellite frames of its own, so GIBS is the only IR source.
- A RainViewer frame has its own path (`/v2/radar/<id>`), so its tiles never change and need no expiry logic.
- The GIBS probe covered a handful of dates and one tile per satellite. The cut-off could be off by a few days.
- Radar older than two hours cannot be recovered, so it must be recorded as it happens. IR can be fetched afterwards for
  any flight in the last 90 days.

## Design

Three parts, cheapest first. Each is useful alone.

### 1. Per-position weather samples (small, and what supports the correlation)

For each stored position, keep a few bytes: the radar class (none, light, moderate, heavy, extreme) and the IR cloud-top
class at that point and time, from the recorded tiles. A new table (for example `wxsample(hex, t, radar, ir)`) holds them,
and the flight's explain and message views can show them.

- Needs the palettes decoded: RainViewer colour scheme 6 (the page uses it) and the GIBS clean-infrared colour map. Neither
  has been looked at yet. The first task is to write them down and test on real tiles.
- Cost is negligible next to the tiles.

### 2. Radar recorder (must be live)

- **Which tiles:** the tiles the aircraft's track passes through during the window, plus the ring of neighbours (about
  3 tiles wide), at zoom 6. The page's native radar zoom is 7; zoom 6 has a quarter of the tiles and is enough to see
  a storm cell near the track. Revisit if the cells look too coarse.
- **When:** a burst every 60 to 90 minutes (never less often than every 2 hours, the whole history window). Each burst reads
  the index once, then fetches the frames at 20-minute spacing (every other 10-minute frame), for the tiles near the part of
  the track covered since the last burst. We know where the aircraft was, so there is no need to guess its route.
- **Which aircraft:** those with an active watch, plus those watched until the next burst, so a landing does not lose the end of its flight.
- **Missed windows:** if the app is down for more than two hours the radar for that time is lost. Log it and move on.
- **Estimated size:** a long flight is about 50 tiles per frame; at 20 minutes that is about 42 frames in 14 hours, so on the
  order of 3 to 4 MB of radar per long flight. This rests on a guessed average tile size of 2 KB, to be replaced with a measured one.

### 3. IR: fetch on demand, keep a filtered copy

- **Inside 90 days:** no recording. When someone asks to see a flight's IR, the page (or server) requests the GIBS tiles for
  the time wanted, the same URLs the live layer builds today with a past time.
- **To outlast the archive:** a one-off job after a flight lands (or, say, 24 hours later) fetches the tiles near its track
  at 30-minute spacing and stores them filtered. That is a handful of requests per flight and no continuous polling.
- **The filter:** keep only the pixels colder than a threshold (the light-blue and colder cloud tops) and make the rest
  transparent. Re-encode as PNG; Go's standard library is enough. A mostly transparent tile should be a few KB, a guess of
  5 to 10 times smaller than 27 KB that has not been tested.
- **Threshold, not gradient.** A gradient filter keeps the edges of a storm and loses its cold core, which is the part
  that matters. The cut-off needs choosing against the colour map once it has been read.
- **Estimated size:** about 35 MB per long flight unfiltered at 30-minute spacing; the filter should make it a few MB.

## Where it is stored

- Tiles as files in a directory (like the backfill cache), named by source, frame time, zoom, x and y; SQLite holds the
  index (`wxframe`: source, frame time, which tiles, bytes, when fetched) and the samples.
- A size cap in GiB with oldest-first eviction, set in `config.yaml`, as `backfill.cache_max_gib` does. Frames for a flight
  that has notes on its aircraft are kept, as the purge command does for other data.
- Off by default; `weather.record: true` turns it on.

## Upstream etiquette (see AGENTS.md)

- Fetch only tiles near tracks followed by an active watch, once per frame, never the whole world.
- Our User-Agent (`FullFlight/<version>`) on every request, a pause between requests, and a limit on concurrent ones.
- Honour 404s and 429/503s with the existing refused-for pause, not retries.
- A tile cache on our server (the OpenStreetMap proposal) is separate and can come first; it does not depend on this.

## What the page shows

- A "Weather at the time" control on a selected message or position: the radar and IR frame nearest that time, with its
  age, drawn in the same layers the live weather uses.
- On the explain page, the weather class at the position beside each note that matters (a turn, a descent).
- Where nothing was recorded (before this was turned on, or in a gap) say so, never show the nearest frame as if it fitted.

## Open questions

Detecting deviations is not part of this: the user notices a deviation and investigates it, with the weather to look at.

1. Zoom 6 or 7 for stored radar tiles.
2. The two colour maps, and the IR cut-off.
3. Whether to draw the stored tiles from our server (`/api/weather/...`) or let the browser ask upstream for IR inside 90 days.
4. Terms: RainViewer's public API terms about storing tiles need reading before anything is kept. Not checked.

## Order of work

1. Read the two palettes and test classifying real tiles (no new fetching beyond a few tiles).
2. Radar recorder and its table, behind the setting, with tests against saved tiles in `testdata/`.
3. Per-position samples from recorded radar; show them in the explain page.
4. IR on demand, then the post-landing filtered copy.
5. The "Weather at the time" control in the page and `scripts/uitest*.js` coverage.
