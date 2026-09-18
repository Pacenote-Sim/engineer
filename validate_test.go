package engineer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

func cornersNumbered(turns ...int) []corner {
	corners := make([]corner, 0, len(turns))
	for _, t := range turns {
		corners = append(corners, corner{Turn: t, DeficitKmh: 14})
	}
	return corners
}

func TestAGoodCuePasses(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	corners := cornersNumbered(1, 4, 9)
	for _, line := range []string{
		"Turn 4, brake later and get the car rotated.",
		"Fourteen down at the apex of Turn 4.",
		"Good lap. Carry more speed into Turn 9.",
		"Brake later.",
		"You are losing the most in Turn 1 — get on the power earlier",
	} {
		r.NoError(ValidateCue("en", CueTraining, line, corners), "rejected: %q", line)
	}
}

// The rule that matters most. A corner number is the one thing in a cue a
// driver acts on immediately and cannot check, so a turn the detector did not
// find is the failure worth being strictest about.
func TestACueNamingATurnThatIsNotThere(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	corners := cornersNumbered(1, 4, 9)

	err := ValidateCue("en", CueTraining, "Brake later into Turn 5.", corners)
	r.Error(err)
	r.ErrorIs(err, plugin.ErrNoAnswer, "the caller must be able to treat this as nothing to say")
	r.Contains(err.Error(), "turn 5")

	// A turn that is there is fine, including the last one.
	r.NoError(ValidateCue("en", CueTraining, "Brake later into Turn 9.", corners))

	// Two turns, one of them invented.
	r.Error(ValidateCue("en", CueTraining, "Turn 1 then Turn 5 are costing you.", corners))

	// No corners at all: any named turn is invented.
	err = ValidateCue("en", CueTraining, "Brake later into Turn 4.", nil)
	r.Error(err)
	r.Contains(err.Error(), "no corners")
	r.Error(ValidateCue("en", CueTraining, "Turn 1 is costing you.", cornersNumbered()))
}

// A line written for one corner may name that corner and no other: the client
// speaks it in front of that corner.
func TestALineForOneCornerNamesOnlyThatCorner(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.NoError(ValidateCornerLine("en", CueTraining, "Brake twenty metres later into Turn 4.", 4))
	r.NoError(ValidateCornerLine("en", CueTraining, "Brake twenty metres later.", 4))

	err := ValidateCornerLine("en", CueTraining, "Brake later into Turn 5.", 4)
	r.Error(err)
	r.ErrorIs(err, plugin.ErrNoAnswer)
	r.Contains(err.Error(), "names turn 5, which is not in this line is for turn 4")

	// The other rules apply the same way.
	r.Error(ValidateCornerLine("en", CueRace, strings.TrimSpace(strings.Repeat("word ", 13)), 4))
	r.Error(ValidateCornerLine("en", CueTraining, "Brake at 40% into Turn 4.", 4))
}

func TestCueWordLimits(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	corners := cornersNumbered(4)
	long := strings.TrimSpace(strings.Repeat("word ", 13))

	// A race cue is heard while racing somebody, so it is the shorter one.
	err := ValidateCue("en", CueRace, long, corners)
	r.Error(err)
	r.Contains(err.Error(), "13 words")
	r.Contains(err.Error(), "limit for cue.race is 12")

	// The same line is fine as a training cue.
	r.NoError(ValidateCue("en", CueTraining, long, corners))

	// Nineteen is not.
	r.Error(ValidateCue("en", CueTraining, strings.TrimSpace(strings.Repeat("word ", 19)), corners))
	// Exactly at the limit is allowed, not one short of it.
	r.NoError(ValidateCue("en", CueRace, strings.TrimSpace(strings.Repeat("word ", 12)), corners))
	r.NoError(ValidateCue("en", CueTraining, strings.TrimSpace(strings.Repeat("word ", 18)), corners))
}

func TestACueThatIsAParagraph(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	corners := cornersNumbered(4)
	r.Error(ValidateCue("en", CueTraining, "One. Two. Three.", corners))
	r.NoError(ValidateCue("en", CueTraining, "One. Two.", corners))

	// A decimal point is not the end of a sentence, and a cue full of lap
	// times is full of decimal points.
	r.NoError(ValidateCue("en", CueTraining,
		"One minute 31.2 seconds. Half a second off your best.", corners))
}

// A speech engine reading "km/h" says "kay em slash aitch", and the driver
// loses the sentence it was in.
func TestACueSpellingUnitsAsSymbols(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	corners := cornersNumbered(4)
	for _, bad := range []string{
		"You are 14 km/h down at the apex.",
		"Fourteen kph down into Turn 4.",
		"Tyres are at 92°.",
		"Brake pressure is 40%.",
	} {
		r.Error(ValidateCue("en", CueTraining, bad, corners), "accepted: %q", bad)
	}

	// The same facts, spelled the way they are spoken.
	r.NoError(ValidateCue("en", CueTraining,
		"Fourteen kilometres per hour down at the apex of Turn 4.", corners))
}

func TestAnEmptyCue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	err := ValidateCue("en", CueTraining, "   ", cornersNumbered(4))
	r.Error(err)
	r.ErrorIs(err, plugin.ErrNoAnswer)

	var cue *CueError
	r.ErrorAs(err, &cue)
	r.Equal("it was empty", cue.Rule)
}
