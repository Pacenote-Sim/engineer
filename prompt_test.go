package engineer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
	"github.com/pacenote-sim/protocol/wire"
)

// What the model is told. These are the tests that matter most in this package
// after the validator: a cue can only be as good as the facts behind it, and the
// commonest cause of a wrong cue is a fact rendered wrongly rather than a model
// behaving badly.

func TestReportFactsSaysWhatHappened(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := reportFacts(aReport(), nil)

	r.Contains(got, "Lap 7, one minute 38.4 seconds.")
	r.Contains(got, "Against your best lap: 0.90 seconds slower.")
	r.Contains(got, "The circuit is 7004 metres round.")
	r.Contains(got, "worst first")
	r.Contains(got, "Turn 1: apex 44 kilometres per hour against 58 on the reference, 14 down")
	r.Contains(got, "still 12 percent brake at the apex")
	r.Contains(got, "braking too late")
	// The corner with nothing conclusive gets its deficit and no diagnosis,
	// which is the commonest and most honest answer.
	r.Contains(got, "Turn 9: apex 110 kilometres per hour against 116 on the reference, 6 down")
	r.NotContains(got, "Turn 9: apex 110 kilometres per hour against 116 on the reference, 6 down; this looks like")
	// A closed setup is not mentioned at all.
	r.NotContains(got, "car")
}

func TestReportFactsOnTheEdges(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A lap with no time is a lap number and nothing invented.
	bare := lapReport{Lap: 3, Session: plugin.SessionPractice, Corners: []corner{{Turn: 2, ApexKmh: 90}}}
	got := reportFacts(bare, nil)
	r.Contains(got, "Lap 3.\n")
	r.NotContains(got, "Against", "no reference, and something was compared")
	r.NotContains(got, "metres round", "no circuit length, and one was stated")

	// A faster lap reads as faster rather than as negative slower, and level
	// is level.
	faster := aReport()
	faster.Reference, faster.DeltaMs = "the class best", -420
	r.Contains(reportFacts(faster, nil), "Against the class best: 0.42 seconds faster.")
	level := aReport()
	level.DeltaMs = 0
	r.Contains(reportFacts(level, nil), "Against your best lap: level.")

	// No corners: the model is told not to name one.
	none := aReport()
	none.Corners = nil
	r.Contains(reportFacts(none, nil), "No corner lost time. Do not name a turn.")

	// The setup open: the earlier notes, or that there are none.
	open := aReport()
	open.SetupOpen = true
	r.Contains(reportFacts(open, nil), "Nothing has been noted about the car this stint.")
	got = reportFacts(open, []string{"The front washes out mid-corner.", "The rear steps out on the throttle."})
	r.Contains(got, "Noted about the car earlier this stint:\n- The front washes out mid-corner.\n- The rear steps out on the throttle.\n")
}

func TestRaceFactsSaysWhatChanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	lap := aLap()
	lap.Number, lap.PersonalBest = 12, true
	lap.Position = &plugin.Position{ClassPos: 4, GapAheadMs: 2100, GapBehindMs: 800}
	prev := &raceLap{Number: 11, DeltaMs: 400, Reference: "your best lap", ClassPos: 5, GapAheadMs: 3400}

	got := raceFacts(lap, prev, []string{"Position changed from P5 to P4."})
	r.Contains(got, "Lap 12, one minute 38.4 seconds.")
	r.Contains(got, "Against your best lap: 0.90 seconds slower.")
	r.Contains(got, "This is the driver's best lap here.")
	r.Contains(got, "Race: P4 in class, 2.10 seconds behind the car ahead, 0.80 seconds ahead of the car behind.")
	r.Contains(got, "Last lap: P5 in class, 3.40 seconds behind the car ahead; 0.40 seconds slower against your best lap.")
	r.Contains(got, "What changed, which is the only reason to speak:\n- Position changed from P5 to P4.\n")

	// An in-lap says so, and a first lap has no last lap.
	lap.Kind, lap.PersonalBest = plugin.LapIn, false
	got = raceFacts(lap, nil, []string{"x"})
	r.Contains(got, "The lap was in, so it does not count.")
	r.NotContains(got, "Last lap")
}

func TestEveryCornerPatternHasWords(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, p := range []wire.CornerPattern{
		wire.PatternEarlyApex, wire.PatternLateBraking, wire.PatternSlowExit,
	} {
		r.NotEmpty(patternWord(p), "the %s pattern reaches the model as nothing", p)
	}
	// An unnamed pattern is the commonest answer, and it is left out rather
	// than guessed at.
	r.Empty(patternWord(""))
	r.Empty(patternWord(wire.CornerPattern("something new")))
}

func TestCornerLineLeavesOutWhatItWasNotTold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A corner with no reference has no deficit worth stating.
	got := cornerLine(corner{Turn: 3, ApexKmh: 88}, spa)
	r.Equal("- Turn 3: apex 88 kilometres per hour.\n", got)

	// And one with everything.
	got = cornerLine(corner{
		Turn: 4, ApexKmh: 70, RefApexKmh: 80, DeficitKmh: 10,
		BrakeAtApex: 8, ThrottleLag: 22, Pattern: wire.PatternSlowExit,
	}, spa)
	r.Contains(got, "a slow exit")
}

// spa is a real circuit length, because the whole point of the conversion is
// that a thousandth of a lap is a different distance at every track.
const spa = 7004

func TestCornerLineSaysDistancesInMetres(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Braking eight thousandths of a lap later than the reference is 56 metres
	// at Spa, rounded to 55. That is a number a driver can act on; "eight
	// thousandths" is not.
	got := cornerLine(corner{
		Turn: 5, ApexKmh: 92, BrakeAtPct: 318, RefBrakeAtPct: 310,
	}, spa)
	r.Contains(got, "braking 55 metres later than the reference")
	r.NotContains(got, "thousandth")

	// Earlier is said as earlier, not as a negative distance.
	got = cornerLine(corner{
		Turn: 5, ApexKmh: 92, BrakeAtPct: 302, RefBrakeAtPct: 310,
	}, spa)
	r.Contains(got, "braking 55 metres earlier than the reference")
	r.NotContains(got, "-55", "a direction is a word, not a minus sign")

	// Throttle, likewise against the reference rather than as a raw lag.
	got = cornerLine(corner{
		Turn: 6, ApexKmh: 80, ThrottleLag: 20, RefThrottleLag: 8,
	}, spa)
	r.Contains(got, "on the throttle 85 metres later than the reference")
}

// Without a circuit length there is no conversion, and inventing one would put
// a number in the driver's ear that no instrument measured.
func TestCornerLineWithoutATrackLengthSaysNoDistance(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := cornerLine(corner{
		Turn: 5, ApexKmh: 92, BrakeAtPct: 318, RefBrakeAtPct: 310, ThrottleLag: 20,
	}, 0)
	// No braking distance at all, rather than a made-up one. ("kilometres"
	// is why this looks for the phrase and not for the word.)
	r.NotContains(got, "braking")
	r.NotContains(got, " metres")
	// The lag is still worth saying, in the only unit available.
	r.Contains(got, "throttle picked up 20 thousandths of the lap after the apex")
}

// The release is the whole of trail braking, and it is the part a driver
// cannot see from the timing screen.
func TestCornerLineSaysTheShapeOfTheRelease(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := cornerLine(corner{
		Turn: 7, ApexKmh: 75, MinKmh: 72, RefMinKmh: 78,
		ExitKmh: 110, RefExitKmh: 121,
		PeakBrakePct: 90, TurnInBrakePct: 75, BrakeAtApex: 10,
		GearAtApex: 2, RefGearAtApex: 3,
	}, spa)
	r.Contains(got, "slowest at 72 against 78")
	r.Contains(got, "leaving at 110 against 121, 11 down on the exit")
	r.Contains(got, "brake 90 percent at its hardest, 75 at turn-in, 10 at the apex")
	r.Contains(got, "2 gear at the apex against 3")

	// With only a peak, it says only the peak.
	got = cornerLine(corner{Turn: 7, ApexKmh: 75, PeakBrakePct: 90}, spa)
	r.Contains(got, "brake 90 percent at its hardest")
	r.NotContains(got, "turn-in")
}

// Rounding to five metres, because the detector is not accurate to one and a
// cue that claims it is, lies.
func TestMetresRoundsToSomethingMeasurable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(metres(0, spa), "no difference is not a distance")
	r.Empty(metres(8, 0), "no track length, no conversion")
	r.Empty(metres(8, -1))
	r.Equal("55 metres", metres(8, spa))
	r.Equal("55 metres", metres(-8, spa), "direction is the caller's to say")
	// 1 thousandth of Spa is 7 metres, which rounds to 5.
	r.Equal("5 metres", metres(1, spa))
	// A thousandth of a kart track is under 2.5 metres, and rounds away.
	r.Empty(metres(1, 1200))
}

func TestStintFactsSaysWhatTheSessionWas(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("No stint.", stintFacts(nil))

	got := stintFacts(aStint())
	r.Contains(got, "14 laps, 11 of them clean.")
	r.Contains(got, "Consistency 82 out of 100.")
	r.Contains(got, "Top speed 241 kilometres per hour.")
	// 44.2 litres over 14 laps: worked out here now, not by the host.
	r.Contains(got, "Fuel 3.16 litres a lap, 12.1 left.")
	r.Contains(got, "Tyres at the end: 92 88 84 83 degrees, spread 9.")
	// Nothing about incidents when there were none, rather than "0 incidents".
	r.NotContains(got, "incident")

	with := aStint()
	with.Incidents = 2
	r.Contains(stintFacts(with), "2 incident(s).")
}

// The camber diagnostic. It is the reason the setup job exists: the spread
// across one tyre is a thing a driver cannot feel and can act on.
func TestSetupFactsNamesTheHotEdge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Contains(setupFacts(nil), "published no setup")

	got := setupFacts(document(t, wire.CarSetup{
		Tyres: []wire.SetupTyre{
			{
				Wheel: wire.WheelLF, ColdKpa: 165, HotKpa: 178.5,
				TempInnerC: 96, TempMiddleC: 90, TempOuterC: 84,
			},
			{
				Wheel: wire.WheelRF, ColdKpa: 165, HotKpa: 177,
				TempInnerC: 84, TempMiddleC: 88, TempOuterC: 95,
			},
			// Within a few degrees is an even tyre and gets no diagnosis.
			{
				Wheel: wire.WheelLR, ColdKpa: 160, HotKpa: 171,
				TempInnerC: 88, TempMiddleC: 87, TempOuterC: 86,
			},
		},
		RearWing: &wire.SetupValue{Name: "Rear wing", Text: "7 hole"},
		Values: []wire.SetupValue{
			{Group: "Aero", Name: "Front splitter", Text: "3"},
			{Name: "Brake bias", Text: "56.5%"},
		},
	}))
	r.Contains(got, "- Rear wing: 7 hole")

	r.Contains(got, "LF: cold 165.0 kPa, hot 178.5 kPa")
	r.Contains(got, "tread 96 / 90 / 84 degrees inner to outer")
	r.Contains(got, "the inner edge is 12 degrees hotter")
	r.Contains(got, "the outer edge is 11 degrees hotter",
		"a tyre hot on the outside is the same diagnostic the other way round")
	r.NotContains(got, "LR: cold 160.0 kPa, hot 171.0 kPa; the")

	// Settings are spelled the way the simulator spells them, so a driver can
	// find the control.
	r.Contains(got, "Aero / Front splitter: 3")
	r.Contains(got, "Brake bias: 56.5%")
}

func TestSpokenLapTimes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// The client's own rendering wins, because it is correct and a model
	// cannot be trusted to produce it.
	r.Equal("one minute 38.4 seconds", spokenOr("one minute 38.4 seconds", 138400))

	// And the fallback for a fact that arrived without one.
	r.Equal("2 minute 18.4 seconds", spokenOr("", 138400))
	r.Equal("42.1 seconds", spokenOr("", 42100))
	r.Equal("no time", spokenOr("", 0))
	r.Equal("no time", spokenOr("", -1))
}

// Nothing shown to a cue job may carry a unit the validator would reject, or
// the model would be quoting something unspeakable straight back.
//
// The setup facts are deliberately not checked: they carry "kPa" and a brake
// bias of "56.5%" because that is what the simulator published, and the two jobs
// that see them — the debrief and the setup advice — are read rather than spoken.
// A cue never sees them.
func TestTheFactsNeverUseSymbolsTheValidatorRefuses(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	open := aReport()
	open.SetupOpen = true
	lap := aLap()
	lap.Position = &plugin.Position{ClassPos: 4, GapAheadMs: 2100, GapBehindMs: 800}
	facts := reportFacts(open, []string{"The front washes out mid-corner."}) +
		raceFacts(lap, &raceLap{ClassPos: 5, DeltaMs: 400, Reference: "your best lap"}, changes(&raceLap{ClassPos: 5}, lap)) +
		stintFacts(aStint())

	for _, line := range strings.Split(facts, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r.False(symbolPattern.MatchString(line),
			"a fact the model is shown carries a unit a cue may not repeat: %q", line)
	}
}

// obj is a node of a schema, which is an object or the schema is wrong.
func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "%v is not an object", v)
	return m
}

func TestTheSchemasAreWellFormed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cue := cueTool(MaxRaceWords)
	r.Equal("cue", cue.Name)
	r.Contains(cue.Description, "12 words")

	// A lap's lines are capped at one per corner given, each for one of those
	// corners; the notes about the car are in the schema only while the setup
	// is open.
	lap := lapTool(MaxTrainingWords, []int{1, 4, 9}, false)
	r.Equal("cues", lap.Name)
	props := obj(t, lap.InputSchema["properties"])
	cues := obj(t, props["cues"])
	r.Equal(3, cues["maxItems"])
	turn := obj(t, obj(t, obj(t, cues["items"])["properties"])["turn"])
	r.Equal([]int{1, 4, 9}, turn["enum"])
	_, asked := props["setup_notes"]
	r.False(asked, "the car was asked about on a fixed setup")
	open := obj(t, lapTool(MaxTrainingWords, []int{1}, true).InputSchema["properties"])
	r.Equal(MaxSetupNotes, obj(t, open["setup_notes"])["maxItems"])

	// A debrief is capped at three findings by the schema rather than by hope,
	// and asks about the car only when there is a sheet.
	dprops := obj(t, debriefTool(false).InputSchema["properties"])
	r.Equal(3, obj(t, dprops["findings"])["maxItems"])
	_, asked = dprops["setup_changes"]
	r.False(asked)
	with := obj(t, debriefTool(true).InputSchema["properties"])
	r.Equal(MaxSetupChanges, obj(t, with["setup_changes"])["maxItems"])

	// A reading names at most three weaknesses, each with a trend that is one
	// of four words.
	prof := profileTool()
	r.Equal("profile", prof.Name)
	weak := obj(t, obj(t, prof.InputSchema["properties"])["weaknesses"])
	r.Equal(MaxWeaknesses, weak["maxItems"])
	trend := obj(t, obj(t, obj(t, weak["items"])["properties"])["trend"])
	r.Equal([]string{"improving", "steady", "worse", "new"}, trend["enum"])
}
