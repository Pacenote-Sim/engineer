package engineer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// The plan is what keeps a line from being cut at the braking point: each
// corner gets the words that fit in front of it, and corners too close for a
// line each share one. The numbers here are worked by hand from the constants.

// A lap of 4000 metres with corners far apart: every corner has room for the
// most a line may have — fifty words in practice, twelve in a race.
func TestFarApartCornersGetTheFullLine(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep := lapReport{
		Session: plugin.SessionPractice, TrackLengthM: 4000,
		Corners: []corner{
			{Turn: 5, ApexPct: 600, ApexKmh: 90, ExitKmh: 120},
			{Turn: 1, ApexPct: 100, ApexKmh: 70, ExitKmh: 110},
		},
	}
	groups := planLines(rep, CueTraining)
	r.Len(groups, 2)
	r.Equal(1, groups[0].Lead().Turn, "the plan is in the order driven")
	r.Equal(5, groups[1].Lead().Turn)
	// Twenty-five seconds of run less two, at the pace a voice reads, is
	// fifty: the straight is longer than the run, so the run is the limit.
	r.Equal(MaxTrainingWords, groups[0].Words)
	r.Equal(50, groups[1].Words)

	race := planLines(rep, CueRace)
	r.Equal(MaxRaceWords, race[0].Words, "a race line is never longer than the race limit")

	// Worst first is the report's order, and the groups can be put back in it.
	back := byReport(groups)
	r.Equal(5, back[0].Lead().Turn)
	r.Equal([]int{1, 5}, leadTurns(groups))
}

// Two corners sixty metres apart at ninety kilometres an hour give the second
// two and a half seconds: one word, less than the shortest line worth
// speaking. It joins the corner before it, and the line is filed under the
// first, with the first's words.
func TestCloseCornersShareOneLine(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep := lapReport{
		Session: plugin.SessionPractice, TrackLengthM: 4000,
		Corners: []corner{
			{Turn: 4, ApexPct: 530, ApexKmh: 70, BrakeAtPct: 515},
			{Turn: 3, ApexPct: 500, ApexKmh: 80},
			{Turn: 1, ApexPct: 100, ApexKmh: 70, ExitKmh: 110},
		},
	}
	groups := planLines(rep, CueTraining)
	r.Len(groups, 2)
	r.Equal([]int{3, 4}, groups[1].Turns())
	r.Equal(3, groups[1].Lead().Turn)
	r.Equal(50, groups[1].Words, "the joined line has the room in front of the first corner")
	r.Equal([]int{1, 3}, leadTurns(groups))
	r.Equal(0, byReport(groups)[0].rank, "the group holding the worst corner comes first")

	// A third corner hard on the second joins the same group.
	rep.Corners = append(rep.Corners, corner{Turn: 5, ApexPct: 545, ApexKmh: 60, BrakeAtPct: 538})
	groups = planLines(rep, CueTraining)
	r.Len(groups, 2)
	r.Equal([]int{3, 4, 5}, groups[1].Turns())
}

// The first corner of the lap never joins anything: the corner before it is on
// the lap before. It gets at least the shortest line, and the client fits it.
func TestTheFirstCornerStandsAlone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep := lapReport{
		Session: plugin.SessionPractice, TrackLengthM: 4000,
		Corners: []corner{
			{Turn: 1, ApexPct: 10, ApexKmh: 70, BrakeAtPct: 4},
			{Turn: 12, ApexPct: 995, ApexKmh: 60, ExitKmh: 90},
		},
	}
	groups := planLines(rep, CueTraining)
	r.Len(groups, 2)
	r.Equal(1, groups[0].Lead().Turn)
	r.Equal(MinLineWords, groups[0].Words)
}

// Without a circuit length there is no time to plan with: every corner stands
// alone with the session's limit, as does the only corner of a lap.
func TestNoLengthMeansNoPlan(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep := lapReport{
		Session: plugin.SessionRace,
		Corners: []corner{{Turn: 3, ApexPct: 500, ApexKmh: 80}, {Turn: 4, ApexPct: 505, ApexKmh: 70}},
	}
	groups := planLines(rep, CueRace)
	r.Len(groups, 2)
	r.Equal(MaxRaceWords, groups[0].Words)
	r.Equal(MaxRaceWords, groups[1].Words)

	one := lapReport{Session: plugin.SessionPractice, TrackLengthM: 4000, Corners: []corner{{Turn: 3, ApexPct: 500}}}
	r.Equal(MaxTrainingWords, planLines(one, CueTraining)[0].Words)
}

// The estimate behind the budget: distance over speed, the speed taken from
// what the report has, and a plain default when it has nothing.
func TestGapSecondsEstimatesFromTheReport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// 1000 metres from apex to braking point, from 72 leaving to 143 on the
	// straight (a quarter more than 115): a mean of 107.5, just under thirty
	// metres a second, is thirty-three and a half seconds.
	prev := &corner{ApexPct: 100, ApexKmh: 72, ExitKmh: 115}
	c := &corner{ApexPct: 400, ApexKmh: 80, BrakeAtPct: 350}
	r.InDelta(33.5, gapSeconds(prev, c, 4000), 0.1)

	// Across the start line the gap wraps: the same 250 thousandths.
	last := &corner{ApexPct: 950, ApexKmh: 72, ExitKmh: 115}
	first := &corner{ApexPct: 250, ApexKmh: 80, BrakeAtPct: 200}
	r.InDelta(gapSeconds(prev, c, 4000), gapSeconds(last, first, 4000), 0.01)

	// No speeds at all: the default, 150, over 400 metres is 9.6 seconds; and
	// braking is assumed thirty thousandths before an apex nobody measured.
	r.InDelta(9.6, gapSeconds(&corner{ApexPct: 0}, &corner{ApexPct: 130}, 4000), 0.01)
	r.Equal(70, brakePoint(&corner{ApexPct: 100}))
	r.Equal(85, brakePoint(&corner{ApexPct: 100, BrakeAtPct: 85}))

	// The words that fit: never above the limit, none when the margin eats it.
	r.Equal(0, wordsThatFit(1, MaxTrainingWords))
	r.Equal(4, wordsThatFit(4, MaxTrainingWords))
	r.Equal(MaxRaceWords, wordsThatFit(60, MaxRaceWords))
}

// What the coach reads: a corner's own budget after its facts, and joined
// corners introduced together with the one budget they share.
func TestGroupFactsSayTheBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	one := cueGroup{Corners: []*corner{{Turn: 1, ApexKmh: 44}}, Words: 9}
	got := groupFacts(one, 4000)
	r.Equal("- Turn 1: apex 44 kilometres per hour. At most 9 words.\n", got)

	two := cueGroup{Corners: []*corner{{Turn: 3, ApexKmh: 80}, {Turn: 4, ApexKmh: 70}}, Words: 11}
	got = groupFacts(two, 4000)
	r.True(strings.HasPrefix(got, "- Turns 3 and 4 are too close together for a line each. Write one line for all of them, "+
		"spoken before Turn 3 and naming each, at most 11 words:\n"), got)
	r.Contains(got, "  - Turn 3: apex 80 kilometres per hour.\n")
	r.Contains(got, "  - Turn 4: apex 70 kilometres per hour.\n")

	r.Equal("Turn 3", turnList([]int{3}))
	r.Equal("Turns 3, 4 and 5", turnList([]int{3, 4, 5}))

	// And the report as a whole says how many lines, and gives every group.
	rep := lapReport{
		Session: plugin.SessionPractice, TrackLengthM: 4000,
		Corners: []corner{
			{Turn: 4, ApexPct: 530, ApexKmh: 70, BrakeAtPct: 515},
			{Turn: 3, ApexPct: 500, ApexKmh: 80},
			{Turn: 1, ApexPct: 100, ApexKmh: 70, ExitKmh: 110},
		},
	}
	facts := reportFacts(rep, nil)
	r.Contains(facts, "at most 2 lines in all")
	r.Contains(facts, "Turns 3 and 4 are too close together")
	r.Contains(facts, "Turn 1: apex 70 kilometres per hour. At most 50 words.")
	r.Less(strings.Index(facts, "Turns 3 and 4"), strings.Index(facts, "Turn 1: apex"), "worst first")

	// The schema asks for no more lines than the lap gets.
	props := obj(t, lapTool(MaxTrainingWords, []int{1, 2, 3, 4, 5, 6}, false).InputSchema["properties"])
	r.Equal(MaxCueLines, obj(t, props["cues"])["maxItems"])
}

// A lap gets few lines, so they go to the corners that cost the most rather
// than to the first corners of the lap. With nothing compared, the client's
// own order stands.
func TestTheWorstCornersAreCoachedFirst(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep := lapReport{
		Session: plugin.SessionPractice, TrackLengthM: 6000,
		Corners: []corner{
			{Turn: 1, ApexPct: 100, ApexKmh: 90, DeficitKmh: 2},
			{Turn: 2, ApexPct: 300, ApexKmh: 80, DeficitKmh: 11},
			{Turn: 3, ApexPct: 500, ApexKmh: 70, DeficitKmh: 4},
			{Turn: 4, ApexPct: 700, ApexKmh: 60, DeficitKmh: 9},
		},
	}
	groups := byReport(planLines(rep, CueTraining))
	r.Equal([]int{2, 4, 3, 1}, leadTurns(groups), "worst first, whatever order they were driven in")

	// The lines a lap gets are capped, so the cap now falls on the corners
	// that gained nothing rather than on the far side of the lap.
	r.Equal(2, groups[0].Lead().Turn)

	// Without a reference there is nothing to rank by and the client's order
	// is kept.
	for i := range rep.Corners {
		rep.Corners[i].DeficitKmh = 0
	}
	r.Equal([]int{1, 2, 3, 4}, leadTurns(byReport(planLines(rep, CueTraining))))

	// The facts say so too, and a lap reported before it ended claims no gap.
	rep.Reference = "your best lap so far this stint"
	facts := reportFacts(rep, nil)
	r.Contains(facts, "Every corner below is compared with your best lap so far this stint.")
	r.NotContains(facts, "level")
	rep.LapMs = 98000
	r.Contains(reportFacts(rep, nil), "Against your best lap so far this stint: level.")
}
