# Working on FullFlight with an LLM agent

FullFlight is a self-hosted tracker: it follows chosen aircraft by joining live ADS-B positions with the ACARS, ADS-C and
CPDLC messages they send, and shows one track on a web map. It is a Go program (module `github.com/avanha/fullflight`)
with a SQLite database and a single-page web UI. Read this file, then `README.md` (what the app does, section by section)
and `HANDOFF.md` (design, data-source findings and decoding gotchas). Both are accurate enough to rely on; the README is
the user-facing reference and must be kept in step with the code.

## Layout

- `cmd/fullflight/` is the entry point (flags include `-config` and `-redecode`).
- `internal/decoder/` turns raw message text into kinds, positions and times (cgo to libacars, plus hand parsers).
  `timeres.go` places partial timestamps (seconds past the hour, time of day) in real time.
- `internal/datalink/` fetches and stores messages, `internal/airframes/` is the airframes.io client,
  `internal/adsb/` and `internal/poller/` are the ADS-B sources and polling, `internal/aerodatabox/` is the optional lookup.
- `internal/flights/` divides an aircraft's history into flights; `internal/airlines/` maps airline codes;
  `internal/flightplan/`, `internal/waypoints/`, `internal/navdata/`, `internal/airports/` hold route and place data.
  `internal/regs/` works out a hex from a US, Canadian or Australian registration and back (the countries whose addresses follow a rule).
- `internal/store/` is SQLite (tables `watch`, `position`, `message`, `fix`, `airframe`, ...), `internal/server/` the HTTP API,
  `internal/server/static/index.html` the whole map page (HTML, CSS and script in one file).
- `scripts/uitest.js` and `scripts/uitest-notes.js` drive the page script against a fake DOM and a recording fake Leaflet.
- `testdata/` real sample messages and API responses. `docs/` is the GitHub Pages home page plus API notes.

## Build and test

    export PKG_CONFIG_PATH=$PWD/third_party/install/lib/pkgconfig   # libacars; built by `make libacars`
    go vet ./... && go test ./...      # = make test
    make uitest                        # needs node; not part of make test

Without that `PKG_CONFIG_PATH`, `go vet` and `go test` fail on libacars. Run the Go tests after any change, and `make uitest`
after any change to `index.html` or its helpers. If node is not installed, say so rather than claiming the page is tested; a
syntax check with another engine is not a test. Add or update a test with each behaviour change, using the existing style
(`internal/flights/flights_test.go` has small rigs; `scripts/uitest.js` has numbered sections).

## Rules that matter for this project

- **The live database is the user's.** `data/fullflight.db` and `config.yaml` are git-ignored and real. Never write to the
  database, run `-redecode`, or start the server against it unasked. To investigate, copy it
  (`sqlite3 -readonly data/fullflight.db ".backup <scratch>/copy.db"`) and read the copy, or query with `sqlite3 -readonly`.
  Tell the user when a fix needs `-redecode` or a restart to reach data already stored.
- **No secrets or data in git.** Never commit API keys, `config.yaml`, databases, or downloaded navdata.
- **Upstream etiquette.** adsb.lol, adsb.fi, airframes.io, RainViewer and NASA GIBS are free services with rate limits.
  Do not add polling, retries or tile requests casually, and never hammer one to debug.
- **Timestamps are the classic failure.** ADS-C gives only seconds past the hour; some stations log a report minutes
  before the time inside it, so the right hour can be the next one. Check any time-resolution change against reports
  logged just before an hour or midnight. A position that arrives with the wrong time looks like a physically impossible track.
- **Real traffic is messy.** Operators write flight numbers differently in messages (`UP65`), callsigns (`UPS65`) and
  schedules (`5X65`); formats vary by airline and satellite header. Prefer to fix the decoder or table than to special-case the UI,
  and add the failing real message to the tests.
- **Say what you did not verify.** If something was not run (UI tests without node, the page in a browser, a live feed),
  report that plainly.

## Style

- Match the surrounding code: comment density, naming and idiom. Comments explain why, in plain sentences; the existing
  code is the guide. Run `gofmt`.
- Change as little as the task needs. The UI is one large file; edit in place rather than restructuring it.
- Keep `README.md` current when behaviour changes (it is organised by feature). `HANDOFF.md` is the original design record.
  Do not rewrite it, but correct a statement that has become wrong.

## Commits and releases

- Commit only when the user asks. Messages are one descriptive line naming what the change does for the user, as in
  `git log`; split unrelated changes into separate commits.
- Releases are annotated tags (`v0.1.0`, `v0.2.0`, ...). Tag only when asked, and do not push anything unasked.
- Both `AGENTS.md` and `CLAUDE.md` exist so different tools find the guidance; `CLAUDE.md` just points here. Edit this one.

## Asking an agent to work on FullFlight: useful prompts

- "N12345's track skips the report at 11:02Z: why?" works well: the agent should look at the stored message (raw text,
  decoded time, flags) in a copy of the database before reading code.
- Name the aircraft (tail or hex) and the time when reporting a wrong track, flight split or missing report.
- To run an agent that uses FullFlight rather than changes it, point it at the HTTP API in `internal/server/server.go`
  (basic auth, see `config.example.yaml`) and give it read-only access.
