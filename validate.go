package engineer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pacenote-sim/plugin"
)

// The cue rules, enforced here rather than only asked for in the prompt.
//
// A prompt is a request and a validator is a guarantee. Everything below is also
// written into the prompt, because a model told the rules follows them nearly
// always — and "nearly always" is the whole problem. A cue is spoken aloud to
// somebody doing 200 km/h; the one in a hundred that names a corner which was
// not in the data is worse than the ninety-nine that were useful were good.
//
// A cue that fails any of these is discarded and nothing is spoken. The client
// has its own template-built line and falls back to it, which is why this can
// afford to be strict: the cost of rejecting a good cue is a plainer cue, and
// the cost of accepting a bad one is a driver turning into a corner that is not
// there.

// The word limits. A race cue is shorter because it is heard while racing
// somebody; these are the rule, and the README repeats them.
const (
	// MaxRaceWords is the ceiling on a cue.race line.
	MaxRaceWords = 12
	// MaxTrainingWords is the ceiling on a cue.training line.
	MaxTrainingWords = 18
	// MaxSentences is how many sentences a cue may be. Two short ones are
	// allowed; a paragraph is not.
	MaxSentences = 2
)

// symbolPattern is the units a speech engine reads badly.
//
// The abbreviations are refused wherever they appear rather than only after a
// digit: "fourteen kph" is read aloud exactly as badly as "14 kph", and the
// first draft of this rule let it through. The degree and percent signs are the
// same problem with one character. The second half is the units that are only
// wrong next to a number — a bare "s" is a word.
var symbolPattern = regexp.MustCompile(`(?i)(\b(km/h|kph|kmh|mph|m/s)\b|[°%]|\d\s*(s|sec|secs)\b)`)

// CueError is a cue that broke one of the rules, with which rule and what it
// said. It exists so a test can assert on the rule rather than on a sentence,
// and so the log line an operator sees names the problem.
type CueError struct {
	// Rule is what was broken, in a few words.
	Rule string
	// Line is the cue that broke it, kept because the only way to improve a
	// prompt is to read what it produced.
	Line string
}

func (e *CueError) Error() string {
	return fmt.Sprintf("engineer: the cue was discarded — %s: %q", e.Rule, e.Line)
}

// Is makes every cue failure match [plugin.ErrNoAnswer]. To the caller there is
// no difference between a model that said nothing and a model that said
// something unusable: both mean speak the fallback, and neither is worth a
// different code path at the moment a driver is waiting.
func (e *CueError) Is(target error) bool { return target == plugin.ErrNoAnswer }

// ValidateCue checks one spoken line against the rules for its job, in the
// language it was written in. corners are what the line was written about, and
// a turn it names has to be one of them; with none, a line that names any turn
// is refused.
func ValidateCue(lang string, kind CueKind, line string, corners []corner) error {
	allowed := make([]int, 0, len(corners))
	for i := range corners {
		allowed = append(allowed, corners[i].Turn)
	}
	return validateLine(lang, kind, line, allowed, "this lap's corners")
}

// ValidateCornerLine checks a line written for one corner. It may name that
// turn and no other: the client speaks it in front of that corner, and a line
// about Turn 5 heard before Turn 4 is a driver braking for the wrong bend.
func ValidateCornerLine(lang string, kind CueKind, line string, turn int) error {
	return validateLine(lang, kind, line, []int{turn}, fmt.Sprintf("this line is for turn %d", turn))
}

// validateLine is the rules. allowed is every turn the line may name, and
// among is how the refusal describes them.
func validateLine(lang string, kind CueKind, line string, allowed []int, among string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return &CueError{Rule: "it was empty", Line: line}
	}

	limit := MaxTrainingWords
	if kind == CueRace {
		limit = MaxRaceWords
	}
	if n := len(strings.Fields(line)); n > limit {
		return &CueError{
			Rule: fmt.Sprintf("%d words, and the limit for %s is %d", n, kind, limit),
			Line: line,
		}
	}

	if n := countSentences(line); n > MaxSentences {
		return &CueError{Rule: fmt.Sprintf("%d sentences, and the limit is %d", n, MaxSentences), Line: line}
	}

	if symbolPattern.MatchString(line) {
		return &CueError{Rule: "it spells a unit as a symbol, which a speech engine reads aloud", Line: line}
	}

	// Every turn it names has to be one it may name. This is the rule that
	// matters most: a corner number is the one thing in a cue a driver acts
	// on immediately and cannot check.
	for _, m := range turnPatternFor(lang).FindAllStringSubmatch(line, -1) {
		if len(allowed) == 0 {
			return &CueError{Rule: "it names a turn and there were no corners to name one from", Line: line}
		}
		if !hasTurn(allowed, m[1]) {
			return &CueError{
				Rule: fmt.Sprintf("it names turn %s, which is not in %s", m[1], among),
				Line: line,
			}
		}
	}
	return nil
}

// hasTurn reports whether the turn a cue named is one it may name.
func hasTurn(allowed []int, want string) bool {
	for _, t := range allowed {
		if fmt.Sprintf("%d", t) == want {
			return true
		}
	}
	return false
}

// countSentences counts terminators rather than splitting, because a decimal
// point is not the end of a sentence and a cue full of lap times is full of
// decimal points. A terminator is one of . ! ? followed by a space or the end
// of the line.
func countSentences(line string) int {
	n := 0
	runes := []rune(strings.TrimSpace(line))
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i == len(runes)-1 {
			n++
			continue
		}
		if runes[i+1] == ' ' {
			n++
		}
	}
	if n == 0 {
		// No terminator at all is one sentence, not none. A cue that does not
		// end in a full stop is a style question and not a safety one.
		return 1
	}
	return n
}
