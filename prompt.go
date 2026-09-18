package engineer

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/pacenote-sim/plugin"
	"github.com/pacenote-sim/protocol/wire"

	"github.com/pacenote-sim/engineer/internal/anthropic"
)

// The prompts, and the facts they are given.
//
// Two rules run through all of this. The model narrates and never computes:
// every number below was calculated exactly, by the client's corner detector or
// the server's SQL, and the job is to turn those into language a person wants in
// their ear at 200 km/h. And a prompt is a request, not a guarantee — everything
// the prompt asks for about a spoken line is also checked in validate.go,
// because the one answer in a hundred that ignores the rules is the one that
// gets spoken to somebody braking.
//
// The facts are rendered as lines of plain text rather than as JSON. A model
// reads either, but a person debugging a bad cue reads one of them, and the
// thing most likely to be wrong with a cue is what it was told.

// PromptVersion is stamped on every answer so a change in what the coach says
// can be traced to a change in what it was asked. Bump it whenever the persona
// or a job prompt changes in a way that could move the output.
const PromptVersion = "2"

// persona is the same on every call, which is what stops the coach being a
// different person between a cue and the debrief that follows it.
//
// It is not cached at the vendor and there is no point wiring that yet: the
// system prompt is persona plus one job's rules, a few hundred tokens, and the
// minimum cacheable prefix is 1024 — 2048 on Haiku, which is one of the three
// models an operator can pick. Caching below the floor is not cheaper, it is
// ignored. An operator who uploads a long persona of their own could cross it,
// and that is the point at which this is worth revisiting.
const persona = `You are a race engineer on the pit wall of a GT endurance team.
You have the driver's telemetry in front of you and about four seconds of their attention.

You are calm, specific and brief. You have raced. You know that a driver who is told three things
does none of them. You never flatter and never catastrophise. When the data does not support a
conclusion you say nothing rather than guess.

Everything you are given has already been measured exactly. Your job is to say it in words, not to
work anything out. Never calculate, estimate or infer a number that is not in front of you, and
never mention a corner that is not in the list you were given.

Never: enthusiasm as filler, apologies, any remark about being an assistant, hedging, or a sentence
that would be equally true of a different lap.`

// cueRules is the half of validate.go that can be asked for in advance. It is
// worded as instructions rather than as a specification because that is what a
// model follows, and it is kept next to the validator so the two do not drift.
const cueRules = `Write one line to be spoken aloud for each corner, just before the driver reaches that corner on
the next lap. Obey all of it:

- One sentence, or two short ones. Never a paragraph.
- Only facts from the input. No invented corner numbers, no invented times.
- A line is about the corner it is for and no other. If you name it, name it as "Turn 4", with that
  corner's own number from the list you were given.
- Spell units as words: "kilometres per hour", never "km/h". No degree or percent signs.
- Second person, present tense. Imperative where it is advice.
- One fix per corner, never two, and make it measurable. "Brake earlier" is worth nothing; "brake
  twenty metres earlier" is an instruction a driver can carry out. Use the distances, pressures,
  speeds and gears from that corner, and only those: where a measurement is missing, say the fix
  without a number rather than guessing one.
- A pressure is a percentage, said as words: "ease to seventy-five percent by turn-in, ten at the
  apex". Round a distance to the nearest five metres and a speed to the nearest whole number;
  nothing here is measured finer than that.
- Leave out a corner with nothing worth saying. A driver told about every corner hears none of them.`

// raceLine and practiceLine are the one sentence that differs between a cue
// during a race and one in practice. They are separate parts because they are
// the shortest thing worth changing.
const raceLine = `This is a race. Position and the gaps either side matter more than the perfect line.`

const practiceLine = `This is practice. The lap time and where it was lost are what matter; there is nobody to race.`

// racePaceRules is the radio.
const racePaceRules = `You are on the radio during a race. The driver hears you between corners, so at most twelve words,
and only because something changed — the list you are given says what.

Say the position, the gap or the pace the way an engineer says it: "P4, car behind at eight tenths",
"half a second off, keep it tidy". Numbers as words or plain digits, never symbols; a gap in seconds
and tenths.

Never coach a corner here; that is for practice. Never name a turn. Never invent a gap or a time that
is not in front of you. If nothing in the list is worth the driver's attention, return an empty line.`

// setupRules is about the car. It is added to a lap's cues while the setup is
// open, and to the debrief when a setup sheet arrived.
const setupRules = `About the car, which can be changed in this session.

Lap by lap: if the same symptom shows in several corners — the front washing out mid-corner, the
rear stepping out on the throttle, the brakes locking, the car not rotating — add one short
observation about the car in setup_notes, worded about the car and not the driver, building on what
was noted earlier this stint. Nothing repeating means an empty list, which is the usual answer.

After the stint, with the setup sheet and the tyre temperatures in front of you: turn what was noted
into at most three changes in setup_changes. Name the control as the simulator spells it, say the
value it had and the value to try, and give the measurement that says why. A change with no
measurement behind it is a guess; leave it out. Say nothing about a control that is not on the sheet.`

// debriefRules is how the written debrief is asked for.
const debriefRules = `Write the debrief the driver reads after the session. The stint is over: nothing here is for now,
everything is for next time out. Two or three sentences on how it went, then at most three findings.
Each finding names what it is about, what the data shows, and one thing to do about it next time.
Say nothing you were not told. If the stint was unremarkable, say so and give fewer findings rather
than padding to three.`

// profileRules is the reading of a driver for the team.
const profileRules = `Read a driver's debriefs, oldest first, as one story. Write two or three sentences an administrator
can act on: what this driver is, what they keep losing time to, what has moved since the earliest
debrief. Then at most three weaknesses that repeat across debriefs — a pattern, not one bad day —
each with the evidence quoted or closely paraphrased from the debriefs, and whether it is improving,
steady, worse or new. Say nothing the debriefs do not say. A driver with one debrief has no trend;
say so and mark everything new.`

// cueTool is the schema one spoken line comes back in. The schema is where the
// length limit lives, so an over-long line is a violation rather than a
// judgement call.
func cueTool(maxWords int) anthropic.Tool {
	return anthropic.Tool{
		Name: "cue",
		Description: fmt.Sprintf(
			"The single line to speak to the driver. At most %d words. Empty when there is nothing worth saying.",
			maxWords),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"line": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("The spoken line, at most %d words, or empty.", maxWords),
				},
			},
			"required": []string{"line"},
		},
	}
}

// lapTool is the schema a lap's cues come back in: one line per corner, each
// for a turn that was in the report, and the notes about the car when the
// setup is open.
func lapTool(maxWords int, turns []int, setupOpen bool) anthropic.Tool {
	props := map[string]any{
		"cues": map[string]any{
			"type":     "array",
			"maxItems": len(turns),
			"description": "One line per corner worth a line, each spoken just before that corner on the " +
				"next lap. Leave out a corner with nothing worth saying.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"turn": map[string]any{"type": "integer", "enum": turns, "description": "The corner the line is for."},
					"line": map[string]any{
						"type":        "string",
						"description": fmt.Sprintf("The spoken line, at most %d words.", maxWords),
					},
				},
				"required": []string{"turn", "line"},
			},
		},
	}
	if setupOpen {
		props["setup_notes"] = map[string]any{
			"type":     "array",
			"maxItems": MaxSetupNotes,
			"items":    map[string]any{"type": "string"},
			"description": "What this lap says about the car, only when the same symptom shows in several " +
				"corners. Usually empty.",
		}
	}
	return anthropic.Tool{
		Name:        "cues",
		Description: "The lines to speak to the driver on the next lap, one per corner.",
		InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"cues"}},
	}
}

// debriefTool is the schema a written debrief comes back in. It is a list of
// findings rather than prose so that the parts can be rendered, stored and
// counted separately — and so that "three findings" is a schema constraint
// rather than a hope. The setup changes are in it only when a sheet arrived.
func debriefTool(withSetup bool) anthropic.Tool {
	props := map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"description": "Two or three sentences on how the stint went overall.",
		},
		"findings": map[string]any{
			"type":     "array",
			"maxItems": 3,
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"area":        map[string]any{"type": "string", "description": "What it is about: a corner, consistency, tyres, fuel."},
					"observation": map[string]any{"type": "string", "description": "What the data shows, in one sentence."},
					"drill":       map[string]any{"type": "string", "description": "One thing to do about it next time out."},
				},
				"required": []string{"area", "observation", "drill"},
			},
		},
	}
	if withSetup {
		props["setup_changes"] = map[string]any{
			"type":     "array",
			"maxItems": MaxSetupChanges,
			"description": "Changes to the car, from the sheet and the measurements. Only a control that is on " +
				"the sheet, and only with a measurement behind it.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"setting": map[string]any{"type": "string", "description": "The control, spelled as the simulator spells it."},
					"from":    map[string]any{"type": "string", "description": "The value it had."},
					"to":      map[string]any{"type": "string", "description": "The value to try."},
					"because": map[string]any{"type": "string", "description": "The measurement that says why, in one sentence."},
				},
				"required": []string{"setting", "to", "because"},
			},
		}
	}
	return anthropic.Tool{
		Name:        "debrief",
		Description: "What to tell the driver after the session.",
		InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"summary", "findings"}},
	}
}

// profileTool is the schema a reading comes back in.
func profileTool() anthropic.Tool {
	return anthropic.Tool{
		Name:        "profile",
		Description: "The coach's reading of one driver over their recent debriefs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "Two or three sentences on who this driver is and what has moved.",
				},
				"weaknesses": map[string]any{
					"type":     "array",
					"maxItems": MaxWeaknesses,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"area":     map[string]any{"type": "string", "description": "What keeps costing time: a corner type, braking, consistency, tyres."},
							"evidence": map[string]any{"type": "string", "description": "What the debriefs said, quoted or close to it."},
							"trend":    map[string]any{"type": "string", "enum": []string{"improving", "steady", "worse", "new"}},
						},
						"required": []string{"area", "evidence", "trend"},
					},
				},
			},
			"required": []string{"summary", "weaknesses"},
		},
	}
}

// reportFacts renders a client's report for the lap's cues. Everything here is
// already exact.
func reportFacts(rep lapReport, notes []string) string {
	var b strings.Builder
	if rep.LapMs > 0 || rep.SpokenLap != "" {
		fmt.Fprintf(&b, "Lap %d, %s.\n", rep.Lap, spokenOr(rep.SpokenLap, rep.LapMs))
	} else {
		fmt.Fprintf(&b, "Lap %d.\n", rep.Lap)
	}
	if rep.Reference != "" {
		fmt.Fprintf(&b, "Against %s: %s.\n", rep.Reference, delta(rep.DeltaMs))
	}
	if rep.TrackLengthM > 0 {
		fmt.Fprintf(&b, "The circuit is %d metres round.\n", rep.TrackLengthM)
	}

	if len(rep.Corners) == 0 {
		b.WriteString("\nNo corner lost time. Do not name a turn.\n")
	} else {
		b.WriteString("\nThe corners that lost time, worst first. Write one line for each that is worth one, to be " +
			"spoken just before the driver reaches it next lap. These are the only turns you may name:\n")
		for i := range rep.Corners {
			b.WriteString(cornerLine(rep.Corners[i], rep.TrackLengthM))
		}
	}
	if rep.SetupOpen {
		b.WriteString("\nThe car can be changed in this session.\n")
		b.WriteString(notesFacts(notes))
	}
	return b.String()
}

// notesFacts is what earlier laps of the stint noted about the car.
func notesFacts(notes []string) string {
	if len(notes) == 0 {
		return "Nothing has been noted about the car this stint.\n"
	}
	var b strings.Builder
	b.WriteString("Noted about the car earlier this stint:\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	return b.String()
}

// raceFacts renders a race lap for the radio: this lap, the one before, and
// what changed between them — which is the only reason to speak.
func raceFacts(lap *plugin.LapFacts, prev *raceLap, why []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Lap %d, %s.\n", lap.Number, spokenOr(lap.SpokenLap, lap.LapMs))
	if lap.Reference != "" {
		fmt.Fprintf(&b, "Against %s: %s.\n", lap.Reference, delta(lap.DeltaMs))
	}
	if lap.PersonalBest {
		b.WriteString("This is the driver's best lap here.\n")
	}
	if lap.Kind != "" && lap.Kind != plugin.LapClean {
		fmt.Fprintf(&b, "The lap was %s, so it does not count.\n", lap.Kind)
	}
	if lap.Position != nil {
		fmt.Fprintf(&b, "Race: %s.\n", position(lap.Position))
	}
	if prev != nil {
		fmt.Fprintf(&b, "Last lap: %s", position(&plugin.Position{
			ClassPos: prev.ClassPos, GapAheadMs: prev.GapAheadMs, GapBehindMs: prev.GapBehindMs,
		}))
		if prev.Reference != "" {
			fmt.Fprintf(&b, "; %s against %s", delta(prev.DeltaMs), prev.Reference)
		}
		b.WriteString(".\n")
	}
	b.WriteString("\nWhat changed, which is the only reason to speak:\n")
	for _, w := range why {
		fmt.Fprintf(&b, "- %s\n", w)
	}
	return b.String()
}

// position says where the car is the way an engineer says it.
func position(p *plugin.Position) string {
	var b strings.Builder
	fmt.Fprintf(&b, "P%d in class", p.ClassPos)
	if p.GapAheadMs > 0 {
		fmt.Fprintf(&b, ", %s behind the car ahead", seconds(p.GapAheadMs))
	}
	if p.GapBehindMs > 0 {
		fmt.Fprintf(&b, ", %s ahead of the car behind", seconds(p.GapBehindMs))
	}
	return b.String()
}

// profileFacts renders a driver's debriefs for a reading, oldest first so that
// a trend reads the way it happened.
func profileFacts(debriefs []Debrief) string {
	list := slices.Clone(debriefs)
	slices.SortStableFunc(list, func(a, b Debrief) int { return a.WrittenAt.Compare(b.WrittenAt) })

	var b strings.Builder
	name := "The driver"
	if len(list) > 0 {
		name = plainOr(list[len(list)-1].DriverName, name)
	}
	fmt.Fprintf(&b, "%s. %d debrief(s), oldest first.\n", name, len(list))
	for i := range list {
		d := &list[i]
		fmt.Fprintf(&b, "\n%s — %s, %s.\n%s\n", d.WrittenAt.Format("2 Jan 2006"),
			plainOr(d.Track, "an unnamed circuit"), plainOr(d.Car, "an unnamed car"), d.Summary)
		for _, f := range d.Findings {
			fmt.Fprintf(&b, "- %s: %s %s\n", plainOr(f.Area, "In general"), f.Observation, f.Drill)
		}
	}
	return b.String()
}

// cornerLine renders one corner: what it lost, and the measurements behind it.
// A measurement the client did not send is left out rather than rendered as
// zero, because a zero is a number and a number gets spoken.
func cornerLine(c corner, lengthM int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- Turn %d: apex %d kilometres per hour", c.Turn, c.ApexKmh)
	if c.RefApexKmh > 0 {
		fmt.Fprintf(&b, " against %d on the reference, %d down", c.RefApexKmh, c.DeficitKmh)
	}
	if c.MinKmh > 0 && c.MinKmh != c.ApexKmh {
		fmt.Fprintf(&b, "; slowest at %d", c.MinKmh)
		if c.RefMinKmh > 0 {
			fmt.Fprintf(&b, " against %d", c.RefMinKmh)
		}
	}
	if c.ExitKmh > 0 && c.RefExitKmh > 0 && c.ExitKmh != c.RefExitKmh {
		fmt.Fprintf(&b, "; leaving at %d against %d, %d down on the exit",
			c.ExitKmh, c.RefExitKmh, c.RefExitKmh-c.ExitKmh)
	}

	// Braking: where it started, against where the reference started.
	if c.BrakeAtPct > 0 && c.RefBrakeAtPct > 0 {
		if d := metres(c.BrakeAtPct-c.RefBrakeAtPct, lengthM); d != "" {
			// A larger ‰ is further round the lap, so braking later.
			word := "later"
			if c.BrakeAtPct < c.RefBrakeAtPct {
				word = "earlier"
			}
			fmt.Fprintf(&b, "; braking %s %s than the reference", d, word)
		}
	}

	// The shape of the release, which is the whole of trail braking.
	switch {
	case c.PeakBrakePct > 0 && c.TurnInBrakePct > 0:
		fmt.Fprintf(&b, "; brake %d percent at its hardest, %d at turn-in, %d at the apex",
			c.PeakBrakePct, c.TurnInBrakePct, c.BrakeAtApex)
	case c.PeakBrakePct > 0:
		fmt.Fprintf(&b, "; brake %d percent at its hardest", c.PeakBrakePct)
	case c.BrakeAtApex > 0:
		fmt.Fprintf(&b, "; still %d percent brake at the apex", c.BrakeAtApex)
	}

	if c.GearAtApex > 0 && c.RefGearAtApex > 0 && c.GearAtApex != c.RefGearAtApex {
		fmt.Fprintf(&b, "; %d gear at the apex against %d", c.GearAtApex, c.RefGearAtApex)
	}

	// Throttle: how late, and how late against the reference.
	switch {
	case c.ThrottleLag > 0 && c.RefThrottleLag > 0:
		if d := metres(c.ThrottleLag-c.RefThrottleLag, lengthM); d != "" {
			word := "later"
			if c.ThrottleLag < c.RefThrottleLag {
				word = "earlier"
			}
			fmt.Fprintf(&b, "; on the throttle %s %s than the reference", d, word)
		} else {
			fmt.Fprintf(&b, "; throttle %d thousandths after the apex against %d",
				c.ThrottleLag, c.RefThrottleLag)
		}
	case c.ThrottleLag > 0:
		if d := metres(c.ThrottleLag, lengthM); d != "" {
			fmt.Fprintf(&b, "; throttle %s after the apex", d)
		} else {
			fmt.Fprintf(&b, "; throttle picked up %d thousandths of the lap after the apex", c.ThrottleLag)
		}
	}

	if word := patternWord(c.Pattern); word != "" {
		fmt.Fprintf(&b, "; this looks like %s", word)
	}
	b.WriteString(".\n")
	return b.String()
}

// metres turns a distance in thousandths of a lap into metres, rounded to five.
// The detector is not accurate to one metre and a cue that claims it is, lies.
// Empty when there is no circuit length to convert with, or nothing to convert.
func metres(perMille, lengthM int) string {
	if lengthM <= 0 || perMille == 0 {
		return ""
	}
	m := perMille * lengthM / 1000
	if m < 0 {
		m = -m
	}
	m = (m + 2) / 5 * 5
	if m == 0 {
		return ""
	}
	return fmt.Sprintf("%d metres", m)
}

// patternWord says a pattern in words. An unnamed pattern is the commonest
// answer and is left out rather than guessed at: a corner that was slower with
// nothing conclusive about why usually wants the deficit and no diagnosis.
func patternWord(p wire.CornerPattern) string {
	switch p {
	case wire.PatternEarlyApex:
		return "an early apex — the car was turned in before the corner arrived"
	case wire.PatternLateBraking:
		return "braking too late — there is still brake on at the apex"
	case wire.PatternSlowExit:
		return "a slow exit — off the brakes but waiting to pick the throttle up"
	}
	return ""
}

// stintFacts renders a session for a debrief.
func stintFacts(s *plugin.StintFacts) string {
	if s == nil {
		return "No stint."
	}
	var b strings.Builder
	// The server does not send a clean-lap count — the summary document has
	// none — so zero is "not told", and a debrief that said "none of them
	// clean" would be saying something nobody measured.
	if s.CleanLaps > 0 {
		fmt.Fprintf(&b, "%d laps, %d of them clean.\n", s.Laps, s.CleanLaps)
	} else {
		fmt.Fprintf(&b, "%d laps.\n", s.Laps)
	}
	fmt.Fprintf(&b, "Best %s, average %s.\n", spokenOr("", s.BestLapMs), spokenOr("", s.AvgLapMs))
	fmt.Fprintf(&b, "Consistency %d out of 100.\n", s.ConsistencyPct)
	if s.Incidents > 0 {
		fmt.Fprintf(&b, "%d incident(s).\n", s.Incidents)
	}
	if s.TopSpeedKmh > 0 {
		fmt.Fprintf(&b, "Top speed %d kilometres per hour.\n", s.TopSpeedKmh)
	}
	// Two figures the host used to work out and no longer does. They are three
	// lines each and they are this plugin's to get right.
	if s.Laps > 0 && s.Fuel.UsedL > 0 {
		fmt.Fprintf(&b, "Fuel %.2f litres a lap, %.1f left.\n", s.Fuel.UsedL/float64(s.Laps), s.Fuel.RemainingL)
	}
	if spread := tyreSpread(s.Tyres); spread > 0 {
		fmt.Fprintf(&b, "Tyres at the end: %.0f %.0f %.0f %.0f degrees, spread %.0f.\n",
			s.Tyres.LF, s.Tyres.RF, s.Tyres.LR, s.Tyres.RR, spread)
	}
	return b.String()
}

// tyreSpread is the hottest tyre minus the coldest, which is the number that
// says whether one corner of the car is working alone. Zero when nothing was
// reported.
func tyreSpread(t plugin.TyreSummary) float64 {
	temps := []float64{t.LF, t.RF, t.LR, t.RR}
	lo, hi := temps[0], temps[0]
	for _, v := range temps[1:] {
		lo, hi = min(lo, v), max(hi, v)
	}
	return hi - lo
}

// setupFacts renders the car. The tread temperatures are the reason setup
// advice exists: the spread across one tyre is the canonical camber
// measurement, and it is a thing a driver cannot feel and can act on.
func setupFacts(doc json.RawMessage) string {
	s := setupOf(doc)
	if s == nil {
		return "The simulator published no setup, so say nothing about the car's settings.\n"
	}
	var b strings.Builder
	b.WriteString("The car as it was driven.\n")
	for _, t := range s.Tyres {
		fmt.Fprintf(&b, "- %s: cold %.1f kPa, hot %.1f kPa; tread %.0f / %.0f / %.0f degrees inner to outer",
			strings.ToUpper(string(t.Wheel)), t.ColdKpa, t.HotKpa, t.TempInnerC, t.TempMiddleC, t.TempOuterC)
		if d := t.TempInnerC - t.TempOuterC; d >= 5 || d <= -5 {
			fmt.Fprintf(&b, "; the %s edge is %.0f degrees hotter", hotEdge(d), abs(d))
		}
		b.WriteString(".\n")
	}
	if s.RearWing != nil {
		fmt.Fprintf(&b, "- Rear wing: %s\n", s.RearWing.Text)
	}
	if len(s.Values) > 0 {
		b.WriteString("\nSettings, spelled as the simulator spells them:\n")
		for _, v := range s.Values {
			if v.Group != "" {
				fmt.Fprintf(&b, "- %s / %s: %s\n", v.Group, v.Name, v.Text)
			} else {
				fmt.Fprintf(&b, "- %s: %s\n", v.Name, v.Text)
			}
		}
	}
	return b.String()
}

func hotEdge(delta float64) string {
	if delta > 0 {
		return "inner"
	}
	return "outer"
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// spokenOr prefers the client's own rendering of a lap time, which is correct,
// over anything built here. A speech engine reading "91240" produces something
// nobody wants in their ear.
func spokenOr(spoken string, ms int) string {
	if spoken != "" {
		return spoken
	}
	if ms <= 0 {
		return "no time"
	}
	m := ms / 60000
	s := float64(ms%60000) / 1000
	if m > 0 {
		return fmt.Sprintf("%d minute %04.1f seconds", m, s)
	}
	return fmt.Sprintf("%.1f seconds", s)
}

// delta says a lap time difference the way an engineer says it.
func delta(ms int) string {
	switch {
	case ms == 0:
		return "level"
	case ms > 0:
		return seconds(ms) + " slower"
	default:
		return seconds(-ms) + " faster"
	}
}

func seconds(ms int) string {
	return fmt.Sprintf("%.2f seconds", float64(ms)/1000)
}
