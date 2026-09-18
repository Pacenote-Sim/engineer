# Testing engineer

Two kinds of test, and both matter. The suite is what CI runs and what a change is judged by. A
drive against a real server is what a driver would see, and it is the only way to read what the
model actually wrote.

## The suite

Everything is a Makefile target, and `make` on its own is `make check`.

| | What | Needs |
|---|---|---|
| `make check` | format, build, vet in both build modes, lint, the tests without a database, every benchmark once, `go mod tidy` is a no-op | the tools below |
| `make test-postgres PACENOTE_TEST_DATABASE_URL=postgres://localhost:5432/postgres` | the whole suite, the store included, with the race detector | a PostgreSQL you may create schemas on — the tests make one each, named `plugin_engineer_test_…`, and drop it |
| `make cover PACENOTE_TEST_DATABASE_URL=…` | coverage with the store: every package over 90 % (`cmd/engineer` excluded — `main()` is the go-plugin handshake, which only a host can run) | the same |
| `make bench` | what a lap costs this plugin besides the model: reading a 40-corner report, rendering its facts, checking a line, ordering the lines, deciding whether the radio speaks | — |
| `make vuln` | govulncheck | — |
| `make dist` | the folder to drop into the server's plugin directory, as `dist/engineer/` | — |

Tools: `gofumpt`, `golangci-lint` v2, `govulncheck`, each under `$(go env GOPATH)/bin`. CI runs the
same targets with a PostgreSQL 17 service, on Go 1.26 and stable.

Without a database the suite passes at about 64 %: the store is half the code and a join cannot be
tested against a fake. With one it is over 90 %, and that is the number the gate holds.

### What the suite asserts, and what it deliberately does not

It never asserts that a prompt produces a particular sentence. That cannot be asserted, and a suite
that tried would fail whenever the vendor improved. The vendor is a fake that answers whatever the
test says, and what is tested is everything around the model:

- **The contract** — a report is read leniently and refused for the right reasons (`contract_test.go`).
- **The corner cues** — the facts the model is shown, and above all what is thrown away: a line for a
  corner not in the report, a line naming another corner, a second line for one corner, thirteen words
  in a race, a unit spelled as a symbol (`engineer_test.go`, `validate_test.go`).
- **The radio** — speaks only when something changed, never on the first lap, never naming a turn; a
  dropped line is not a failed event (`race_test.go`).
- **The debrief and the setup** — changes asked for only with a sheet and the setting on, kept within
  three, the laps' notes in front of the model (`engineer_test.go`).
- **The reading** — written once when stale, served from the file otherwise, never written from the
  driver's own page (`profile_test.go`, `store_postgres_test.go`).
- **The pages** — who may reach what, and that nothing the model or a driver wrote reaches a page as
  markup (`http_test.go`).
- **The store** — every round trip, the joins to the server's `drivers` view, the retention, a driver
  reading only their own (`store_postgres_test.go`).
- **The manifest** — loaded the way the host loads it, every route declared for the right caller.

## Against a server

What you need: a Pacenote server running with its panel, an Anthropic key, and a terminal. The
workspace's `try/engineer.sh` does every step below as a command — `try/engineer.sh all` is the happy
path, `try/engineer.sh help` lists the rest — and `try/drive.sh` is the same in one go.

### 0. From nothing: the server

1. **PostgreSQL 15 or newer**, with an empty database and a user allowed to create tables:
   `createdb pacenote` on a local Homebrew or apt PostgreSQL is enough.
2. **The server binary**: a release from `github.com/Pacenote-Sim/server`, or from a checkout
   `make build` in `server/`, which leaves `./pacenote-server` beside the Makefile.
3. **Start it**: `./pacenote-server`. On the first run it prints a setup token and waits. Open
   `http://localhost:8080/setup`, type the token, and the wizard asks for the connection string
   (`postgres://you@localhost:5432/pacenote`), your organisation's name, an administrator email and
   password, and the public address — `localhost:8080` behind "my own proxy" is fine for a machine on
   your desk. It builds the schema and starts serving; nothing to restart.
4. It now has `pacenote-data/` beside it, with `config.json` and an empty `plugins/`. Sign in at
   `/admin`.

If it was set up once already, `./pacenote-server` just starts. With no `config.json` and no
`PACENOTE_DATABASE_URL`, it stops and says so rather than running the wizard against a database that
may already hold an administrator.

### 1. Build and install the plugin

```
make dist
cp -R dist/engineer <the server's>/pacenote-data/plugins/engineer
```

The folder's name and the name in `plugin.json` have to match. `make dist` builds for the machine it
runs on; a server on another platform wants `GOOS`/`GOARCH` set for the `go build` in that target. In the panel: *Plugins → Look for new plugins*,
then enable it. The host creates a schema for it and applies the four migrations; the plugin's page
in the panel shows what it declared: two events, six routes, a database, network.

### 2. Configure

On the plugin's settings page: the Anthropic key. Leave the four switches on and the retention at
180. Save.

To see what "not configured" looks like, skip the key for the first run: a posted lap answers `503`
and the panel shows the plugin needing attention. Nothing else breaks.

### 3. Drive a stint

From the workspace root, with the server at `http://localhost:8080`:

```
BASE=http://localhost:8080 try/drive.sh
```

The first run pairs a device: approve it at `/admin/pairings`. The token is kept in `try/.token` for
next time. The script then does what the client does, and says what to look for after each step:

| Step | What happens | What you should see |
|---|---|---|
| open a **race** stint at Spa, with a setup sheet | `PUT /stints/{id}` | `HTTP 200` |
| post four laps | `POST /stints/{id}/laps` | the server's reply. Each lap is a `lap.completed` to engineer: lap 1 is the first of the stint and says nothing; lap 3 is a personal best of the stint, so **the radio speaks** — one call, twelve words at most, filed as lap 3's turn-0 line; lap 4 is 0.44 s off, under the half-second, so silence |
| post lap 4 to **engineer's own route** | `POST /plugin/engineer/laps` | JSON: `cues` for Turn 1 and Turn 8 in track order with their `apex_pct`, `mode: cue.race`, and — the setup being open — maybe a `setup_notes` entry about the car |
| finish the stint | `PUT /stints/{id}/summary` | `HTTP 200`; the server tells engineer, which writes the debrief and, the sheet being there, up to three setup changes |
| read the debrief back | `GET /plugin/engineer/debrief?stint={id}` | JSON with `summary`, `findings`, `setup_changes`, a few seconds after the finish. The script asks up to six times |

Each call's cost is on the plugin's panel page, by job: `racepace`, `cues`, `debrief`.

### 4. The pages

| Address | Sign in as | What to check |
|---|---|---|
| `/plugin/engineer/` | an administrator | the driver's row: two debriefs after two runs, "Not read yet." |
| `/plugin/engineer/drivers/<slug>` | an administrator | opening it **writes the reading** — one `profile` call on the panel — then shows the debriefs under it. Reload: served from the file, no call. Run the script again, reload the overview: "a newer debrief since this was read"; open the driver again: rewritten, one call |
| `/plugin/engineer/me` | the driver | the reading if one exists, **lap by lap** (Turn 1, Turn 8, the radio line, "About the car"), the debriefs with their setup changes. From a terminal it is the same page with the client's token: `curl -H "authorization: Bearer $(cat try/.token)" http://localhost:8080/plugin/engineer/me` |
| `/plugin/engineer/agent` | an administrator | eight parts. Change one and save; the next answer's `prompt_version` is `2+` and a digest, on the page and in the JSON |
| `/admin/plugins/engineer` | an administrator | what it spent, by job and model; what it printed |

### 5. Things worth breaking

- **Turn the radio off** (*Speak on the radio in a race*), drive again: no `racepace` row, no cost.
- **Turn setup advice off**: the debrief comes with no `setup_changes`, and a posted lap with
  `setup_open: true` carries no notes.
- **Wrong key**: the lap answers `502`, the panel log says the vendor refused and that it will keep
  happening; nothing is spoken.
- **Post the same lap twice**: the second answer replaces the first in `GET /cues`, not beside it.
- **Another driver's token** on `GET /cues?stint=…` or `/debrief?stint=…`: `404`, not their data.
- **A bad report**: change `"lap": 4` to `0` in the script — `400`, and the body says what is wrong.
- **Change `"session": "race"` to `"practice"`** on the posted lap: 18-word lines instead of 12.

### 6. What cannot be shown yet

- **Position and gaps on the radio.** The server's lap facts carry the delta to the reference and a
  personal best; they carry no race position, because the client does not send one yet. Until it
  does, the radio speaks on a personal best and on a half-second drop, and the position, gap and
  "car behind" lines in `race.go` wait for their data.
- **Real measurements.** The eleven fields per corner in `drive.sh` are typed in. The Windows client
  will compute them from the simulator; this plugin cannot tell the difference and does not try to.
- **Speech.** The lines are text. Speaking them is the client's.

### 7. Cleaning up

Disable and remove the plugin in the panel: the host drops its schema whole, cues, debriefs,
readings and prompt alike. Or lower *Keep debriefs and cues for* and finish a stint — the retention
runs after each debrief.

## Licence

Engineer is an official Pacenote plugin and, like the server, is licensed under the GNU General
Public License, version 3 — see `LICENSE`. The plugin contract it is built on
(`github.com/pacenote-sim/plugin`) stays Apache-2.0, so that anyone may write a plugin.
