package engineer

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// A line is spoken on the way to its corner and has to be over before the
// driver brakes; a line still going at the braking point is cut, and a cut
// line is worse than none. So before the coach is asked, the lap is planned:
// each corner gets the number of words that fit in front of it, and corners
// too close together for a line each are joined into one line, spoken before
// the first of them and naming them all. The client keeps the same numbers
// and skips what would not fit anyway, so nothing here is a guarantee; it is
// what makes the lines fit in the first place.

// How a line is spoken. These match the client, which counts the same way.
const (
	// WordsPerSecond is the pace a line is really spoken at. It is not a guess:
	// the voice vendor meters 750 characters to the minute of audio, and a
	// coaching line runs about 5.7 characters a word, which is 132 words a
	// minute. Budgeting for a faster reader is how a line that was meant to
	// end before the braking point ends in the braking zone.
	WordsPerSecond = 2.2
	// LeadSeconds is the longest run the client gives a line: it raises the
	// corner's cue this far before the braking point and then holds the line
	// until the moment it has to start. A line longer than this cannot be
	// spoken whole however long the straight.
	LeadSeconds = 25.0
	// SpeakMargin is what a line must leave free before the braking point: the
	// client's second of silence, the voice's own lead-in, and slack for a
	// corner arrived at faster than the lap before. With LeadSeconds it sets
	// the longest line there can be, which is why it matches
	// [MaxTrainingWords].
	SpeakMargin = 2.0
	// MinLineWords is the shortest line worth a corner of its own — "Turn 4,
	// brake later." A corner with room for fewer joins the corner before it.
	MinLineWords = 4
	// MaxCueLines is how many lines a lap gets. A driver told about every
	// corner hears none of them; the worst corners come first in a report
	// and the coach is told to take those.
	MaxCueLines = 4
	// DefaultBrakeLeadPct is how far before the apex braking is assumed to
	// start, in thousandths of the lap, when the report did not measure it.
	DefaultBrakeLeadPct = 30
	// DefaultStraightKmh is the speed assumed between two corners when the
	// report carries no speed to estimate it from.
	DefaultStraightKmh = 150
)

// cueGroup is one line's worth of corners: one, or several too close for a
// line each, in the order driven. The line is spoken before the first.
type cueGroup struct {
	Corners []*corner
	// Words is the most the line may have, so that it is over before the
	// driver brakes for the first corner.
	Words int
	// rank is where the group's worst corner stood in the report, for
	// rendering the groups worst first as the report was.
	rank int
}

// Lead is the corner the line is spoken before and filed under.
func (g cueGroup) Lead() *corner { return g.Corners[0] }

// Turns is every turn the line may name.
func (g cueGroup) Turns() []int {
	out := make([]int, 0, len(g.Corners))
	for _, c := range g.Corners {
		out = append(out, c.Turn)
	}
	return out
}

// planLines groups a report's corners into lines and gives each its words.
//
// The corners are taken in the order driven. The room in front of a corner is
// the time from the previous corner's apex to this corner's braking point, at
// the speed the car is estimated to carry between them; the words that fit in
// that time, less the margin, are the line's budget, and never more than the
// lead the client speaks with or the limit for the session. A corner with room
// for fewer than MinLineWords joins the corner before it. The first corner of
// the lap never joins: the corner before it is on the lap before, and its line
// is the next lap's. Without a circuit length there is no time to plan with,
// and every corner gets the session's limit.
func planLines(rep lapReport, kind CueKind) []cueGroup {
	limit := wordsFor(kind)
	order := make([]*corner, 0, len(rep.Corners))
	for i := range rep.Corners {
		order = append(order, &rep.Corners[i])
	}
	rank := ranks(rep.Corners)
	sort.SliceStable(order, func(i, j int) bool { return order[i].ApexPct < order[j].ApexPct })

	groups := make([]cueGroup, 0, len(order))
	for i, c := range order {
		words := limit
		if rep.TrackLengthM > 0 && len(order) > 1 {
			prev := order[(i+len(order)-1)%len(order)]
			words = wordsThatFit(gapSeconds(prev, c, rep.TrackLengthM), limit)
		}
		if i > 0 && words < MinLineWords {
			g := &groups[len(groups)-1]
			g.Corners = append(g.Corners, c)
			g.rank = min(g.rank, rank[c.Turn])
			continue
		}
		groups = append(groups, cueGroup{Corners: []*corner{c}, Words: max(words, MinLineWords), rank: rank[c.Turn]})
	}
	return groups
}

// wordsThatFit is the words spoken in the given seconds, less the margin, and
// never more than the client's lead or the session's limit allows.
func wordsThatFit(seconds float64, limit int) int {
	room := math.Min(seconds, LeadSeconds) - SpeakMargin
	if room <= 0 {
		return 0
	}
	return min(int(math.Floor(room*WordsPerSecond)), limit)
}

// gapSeconds is the estimated time from the previous corner's apex to this
// corner's braking point.
//
// The speed over that stretch is estimated from what the report has: the car
// leaves the previous apex at its apex speed and gains speed to the straight,
// taken as a quarter more than the higher of the previous exit speed and this
// apex speed — a car keeps gaining speed after the exit, and a quarter errs
// toward a shorter line, which is the safe side. The mean of the two is the
// speed over the gap. A report with no speeds at all gets a plain default.
func gapSeconds(prev, c *corner, lengthM int) float64 {
	perMille := brakePoint(c) - prev.ApexPct
	for perMille <= 0 {
		perMille += 1000
	}
	metres := float64(perMille) * float64(lengthM) / 1000

	straight := max(prev.ExitKmh, c.ApexKmh, c.MinKmh)
	if straight <= 0 {
		straight = DefaultStraightKmh
	} else {
		straight = straight * 5 / 4
	}
	mean := float64(straight)
	if prev.ApexKmh > 0 {
		mean = float64(prev.ApexKmh+straight) / 2
	}
	return metres / (mean / 3.6)
}

// brakePoint is where braking starts for a corner, in thousandths of the lap:
// measured when the report has it, assumed a little before the apex when not.
func brakePoint(c *corner) int {
	if c.BrakeAtPct > 0 {
		return c.BrakeAtPct
	}
	return c.ApexPct - DefaultBrakeLeadPct
}

// ranks is where each corner stands in what it cost, worst first. A lap gets
// few lines, so which corners they go to matters more than their order: a
// client that sends its corners in the order driven still gets the corners
// that lost the most time coached. With nothing lost anywhere — a report with
// no reference to compare against — the order stands as the client sent it.
func ranks(corners []corner) map[int]int {
	order := make([]int, 0, len(corners))
	for i := range corners {
		order = append(order, i)
	}
	sort.SliceStable(order, func(a, b int) bool {
		return corners[order[a]].DeficitKmh > corners[order[b]].DeficitKmh
	})
	out := make(map[int]int, len(corners))
	for place, i := range order {
		out[corners[i].Turn] = place
	}
	return out
}

// byReport orders groups the way the report was: worst first.
func byReport(groups []cueGroup) []cueGroup {
	out := make([]cueGroup, len(groups))
	copy(out, groups)
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })
	return out
}

// leadTurns is the turn each group's line is filed under.
func leadTurns(groups []cueGroup) []int {
	out := make([]int, 0, len(groups))
	for i := range groups {
		out = append(out, groups[i].Lead().Turn)
	}
	return out
}

// groupFacts renders one group for the prompt: the corner with its word
// budget, or the joined corners with the one budget they share.
func groupFacts(g cueGroup, lengthM int) string {
	if len(g.Corners) == 1 {
		line := cornerLine(*g.Corners[0], lengthM)
		return strings.TrimSuffix(line, ".\n") + fmt.Sprintf(". At most %d words.\n", g.Words)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- %s are too close together for a line each. Write one line for all of them, "+
		"spoken before Turn %d and naming each, at most %d words:\n", turnList(g.Turns()), g.Lead().Turn, g.Words)
	for _, c := range g.Corners {
		b.WriteString("  " + cornerLine(*c, lengthM))
	}
	return b.String()
}

// turnList is "Turns 3 and 4", or "Turns 3, 4 and 5".
func turnList(turns []int) string {
	parts := make([]string, len(turns))
	for i, t := range turns {
		parts[i] = fmt.Sprint(t)
	}
	if len(parts) == 1 {
		return "Turn " + parts[0]
	}
	return "Turns " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
