# engineer

A Pacenote server plugin that analyses the team's driving. It reads each driver's laps and stints
as they arrive and turns the numbers into words a driver can act on: a line for the corner ahead in
practice, the radio in a race, a debrief with setup changes after the stint, and — for the team —
a reading of what each driver keeps losing time to. The words are written by Anthropic's Claude with
the operator's own key; every number in them was measured by the client or the server first, and the
plugin invents nothing.

It runs inside a Pacenote server as a separate program. The client that measured a lap posts to this
plugin's own address; everything else arrives as the server's events. The server is not changed for
it.

## What it does

| | When | What |
|---|---|---|
| **Corner cues** | the client posts a lap it just measured | one spoken line per corner that lost time, to be spoken just before that corner on the next lap. At most 18 words each (12 in a race), one call per lap |
| **The radio** | a race lap completes | at most 12 words, and only when something changed: a position, a car closing to within a second, a personal best, a lap half a second off the one before. Silence otherwise |
| **Debrief** | a stint finishes | two or three sentences and up to three findings, each with what the data showed and one thing to do about it. Kept |
| **Setup changes** | with the debrief, when the simulator published the setup sheet and the session let it be changed | up to three changes — the control as the simulator names it, from what it was to what to try, and why. Built from what the coach noted about the car lap by lap |
| **A reading of each driver** | an administrator opens the driver's page and there is a new debrief to read | what this driver keeps losing time to, with the evidence, and whether it is moving |

## Installing it

```
go build -o engineer ./cmd/engineer
```

Put `engineer`, `plugin.json` and `migrations/` in a folder called `engineer` inside the server's
plugin directory, then press *Look for new plugins* in the panel. The folder's name and the name in
`plugin.json` have to match.

## Configuring it

On the plugin's page in the panel:

| Setting | |
|---|---|
| **Anthropic key** | Required. From console.anthropic.com. Sealed by the server, lent to this plugin one call at a time. |
| **Model** | Sonnet, Opus or Haiku. Try the middle one first. |
| **Write a debrief after every stint** | On by default. The most expensive thing this does. |
| **Speak on the radio in a race** | On by default. |
| **Suggest setup changes** | On by default. Nothing happens on a fixed setup either way. |
| **Speak the lines** | On by default. Asks the `voice` plugin, when installed, for every line's audio. |
| **Language** | English by default. What the coach writes in — lines, debrief, reading — and what it tells `voice` the words are in, so the two never have to be set twice. A corner is checked under that language's word for it ("Curva 4"). |
| **Keep debriefs and cues for** | Days. A season is about 180. Zero keeps them for ever. |

Without a key the driver still gets cues — the client speaks its own — and the pages still show
what was written before.

## Its pages

| Address | Who | What |
|---|---|---|
| `/plugin/engineer/` | an administrator | every driver the coach has written about, with the reading of each |
| `/plugin/engineer/drivers/<slug>` | an administrator | the reading of one driver, written when opened if there is something new, then their debriefs |
| `/plugin/engineer/agent` | an administrator | what the coach is asked, editable |
| `/plugin/engineer/me` | a signed-in driver | their own: the reading, what was said lap by lap, their debriefs and setup changes |

## What a client sends, and gets back

A client plugin — the Windows telemetry app — is the driver, with the same device token it uploads
laps with. The host checks the token and tells this plugin which driver; the token never reaches
it. A driver signed in to the server's own pages reaches the same addresses from a browser.

### `POST /plugin/engineer/laps`

The lap just finished, as the client measured it against a reference. The lines come back in the same
answer, in the order they are heard, each with where on the lap to speak it — see *When to post*
below for the moment to send it.

```json
{
  "stint_id": "7f0c2e1a-3b4d-4e5f-8a9b-0c1d2e3f4a5b",
  "lap": 7,
  "lap_ms": 138400,
  "spoken_lap": "one minute 38.4 seconds",
  "session": "practice",
  "track_length_m": 7004,
  "reference": "the team's best lap in this car",
  "delta_ms": 900,
  "setup_open": true,
  "corners": [
    {
      "turn": 4, "apex_pct": 310,
      "apex_kmh": 92, "ref_apex_kmh": 104, "deficit_kmh": 12,
      "min_kmh": 88, "ref_min_kmh": 96,
      "exit_kmh": 141, "ref_exit_kmh": 152,
      "brake_at_pct": 288, "ref_brake_at_pct": 296,
      "peak_brake_pct": 90, "turn_in_brake_pct": 75, "brake_at_apex": 10,
      "throttle_lag": 20, "ref_throttle_lag": 8,
      "gear_at_apex": 2, "ref_gear_at_apex": 3,
      "pattern": "late_braking"
    }
  ]
}
```

| Field | | |
|---|---|---|
| `stint_id` | required | the server's own id for the stint, from the upload. How the lap's cues are filed and fetched |
| `lap` | required | the simulator's lap counter, from 1 |
| `lap_ms`, `spoken_lap` | | the lap time, and the client's own rendering of it — a model cannot be trusted to produce "one minute 38.4 seconds" |
| `session` | required | `practice`, `qualifying`, `race` or `testing`. A race gets the 12-word limit |
| `track_length_m` | | metres round the circuit. Turns a thousandth of a lap into "brake twenty metres later". Without it, no distances are said |
| `reference` | | what every corner was compared against, in words a driver would use. The client chooses the reference — own best, team best, class best — and says which; this plugin repeats the name |
| `delta_ms` | | the lap against the reference, positive is slower |
| `setup_open` | | the car can be changed in this session. When true the coach notes what the car is doing, lap by lap, for the setup changes after the stint. On a fixed setup leave it out and nothing about the car is said |
| `corners` | | the corners that lost time, worst first. At most 40; an empty list is a lap with nothing to coach, which costs nothing |

Each corner: `turn` (required, from 1) and `apex_pct` (where the apex is, in thousandths of the lap,
0–1000) place it; everything else is optional and left out when not measured — a zero is a number
and a number gets spoken. Speeds in whole km/h: `apex_kmh`, `min_kmh`, `exit_kmh` and their `ref_`
counterparts on the reference lap; `deficit_kmh` is the apex speed lost. Braking: `brake_at_pct`
and `ref_brake_at_pct` are where braking started, in thousandths of the lap; `peak_brake_pct`,
`turn_in_brake_pct` and `brake_at_apex` are pressures in percent. Throttle: `throttle_lag` and
`ref_throttle_lag` are thousandths of the lap between the apex and the throttle coming back.
`gear_at_apex` and `ref_gear_at_apex`. `pattern` is the protocol's, when the detector named one.
Fields this plugin does not know are ignored, so a client may measure more before the coach reads it.

The answer:

```json
{
  "stint_id": "7f0c2e1a-…", "lap": 7, "session": "practice", "mode": "cue.training",
  "reference": "the team's best lap in this car",
  "cues": [
    {"turn": 4, "apex_pct": 310, "line": "Brake twenty metres later and ease to seventy-five percent by turn-in."}
  ],
  "setup_notes": ["The front washes out mid-corner in the slow corners."],
  "model": "claude-sonnet-5", "prompt_version": "2", "written_at": "2026-09-16T10:00:00Z"
}
```

`cues` is in the order they are heard; speak each as its `apex_pct` approaches. A corner with
nothing worth saying is not in it. When the **voice** plugin is installed and configured, each cue
also carries `audio` (base64) and `audio_type` (`audio/mpeg` or `audio/wav`) — the line already
spoken, so the client plays it rather than reading it; without voice the words go alone and the
client speaks or shows them itself. Speaking can be turned off in engineer's settings. `setup_notes` is what this lap added about the car, only when
`setup_open` was true. The response also carries what the call cost, which the server meters.

| Status | |
|---|---|
| `200` | the lines, possibly none |
| `400` | the report could not be read; the body says what is missing |
| `401` | no signed-in driver |
| `502` | the coach could not answer — the vendor refused or the deadline passed. Speak your own line |
| `503` | the operator has not set a key. Speak your own line |

**When to post.** Not at the line: after the last corner. Once the final apex of a lap is behind
the car every measurement in the report exists, and the straight to the line changes nothing — so
post then, and the whole straight is time for the coach and the voice to answer before Turn 1 of the
next lap arrives. `lap_ms` and `delta_ms` are optional for exactly this reason. If a lap's lines are
still not back when Turn 1 approaches, play the previous lap's Turn 1 line and pick the new ones up
from Turn 2: never late, never silent.

### `GET /plugin/engineer/cues?stint=<id>&lap=<n>`

What was written about a lap, again: after a reconnect, or for the radio line, which is written from
the server's own facts and no client posted. Without `lap` it is the latest lap of the stint. The
same shape as above, with the radio line as a cue whose `turn` is `0`. `404` when nothing was
written. A driver reads only their own.

### `GET /plugin/engineer/debrief?stint=<id>`

The debrief for a stint, for the client app the driver opens when the stint is over:

```json
{
  "stint_id": "7f0c2e1a-…", "track": "Spa", "car": "GT3",
  "summary": "…", "findings": [{"area": "Turn 1", "observation": "…", "drill": "…"}],
  "setup_changes": [{"setting": "Rear wing", "from": "7", "to": "6", "because": "…"}],
  "model": "claude-sonnet-5", "prompt_version": "2", "written_at": "…"
}
```

`404` until it has been written, which is a few seconds after the stint's summary reaches the server.

## The rules a spoken line obeys

Whatever the prompt says, a line is checked in code after the model answers, and one that fails is
dropped rather than spoken. The client speaks its own line instead, so the coach can afford to be
strict.

- At most **18 words** in practice, qualifying and testing; **12** in a race, on the radio and off it.
- At most **two sentences**.
- Units as words. Never `km/h`, `kph`, `mph`, `°` or `%` — a speech engine reads them aloud.
- A corner line may name **its own turn and no other**; the radio may name none.
- A line for a corner that was not in the report, or a second line for the same corner, is dropped.

## Changing what the coach says

At `/plugin/engineer/agent` you can rewrite any part of what it is asked — who the coach is, how the
corner lines are asked for, what differs in a race, the radio, what to notice about the car, how the
debrief reads, how a driver is read — in the page or by attaching a markdown file. Anything you leave
alone stays as it ships, and goes back with one button. Every answer is stamped with the version of
the prompt that produced it.

The rules above are enforced whatever you write.

## Its own tables

It gets a PostgreSQL schema of its own — debriefs, what was said lap by lap, the reading of each
driver, the prompt — joined to your drivers in one query. Removing the plugin drops it.

## Testing it

`make` runs what CI runs. `TESTING.md` is the guide: the suite, the coverage gate, and a drive against
a real server step by step, with what to look for and what to break.

## Licence

GNU General Public License, version 3 — see `LICENSE`. Like the server. The plugin contract it is
built on (`github.com/pacenote-sim/plugin`) stays Apache-2.0, so that anyone may write a plugin.
