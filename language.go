package engineer

import (
	"regexp"
	"strings"
	"sync"
)

// The languages the coach writes in.
//
// A language is a setting on this plugin and nowhere else: the coach writes the
// words, so it knows what they are in, and it tells whoever speaks them. What
// the language changes here is two things — the sentence the model is given,
// and the word the validator looks for in front of a corner number, because a
// rule that only knows "Turn 4" would let an invented "Curva 9" through.

// language is one the coach can write in.
type language struct {
	Code string
	Name string
	// turnWords are what a corner is called in front of its number, lowercase.
	// English is always accepted: "Turn 4" is a proper noun on most circuits.
	turnWords []string
}

// languages is the list offered, English first.
var languages = []language{
	{Code: "en", Name: "English", turnWords: []string{"turn"}},
	{Code: "es", Name: "Spanish", turnWords: []string{"curva", "turn"}},
	{Code: "de", Name: "German", turnWords: []string{"kurve", "turn"}},
	{Code: "fr", Name: "French", turnWords: []string{"virage", "turn"}},
	{Code: "it", Name: "Italian", turnWords: []string{"curva", "turn"}},
	{Code: "pt", Name: "Portuguese", turnWords: []string{"curva", "turn"}},
	{Code: "nl", Name: "Dutch", turnWords: []string{"bocht", "turn"}},
	{Code: "pl", Name: "Polish", turnWords: []string{"zakręt", "zakret", "turn"}},
}

// DefaultLanguage is English.
const DefaultLanguage = "en"

// languageOf is the language for a code, English for one that is not offered.
func languageOf(code string) language {
	for _, l := range languages {
		if l.Code == code {
			return l
		}
	}
	return languages[0]
}

var (
	turnPatterns   = map[string]*regexp.Regexp{}
	turnPatternsMu sync.Mutex
)

// turnPatternFor finds every corner a line names in the given language, with
// the number in the first group. Compiled once per language.
func turnPatternFor(code string) *regexp.Regexp {
	l := languageOf(code)
	turnPatternsMu.Lock()
	defer turnPatternsMu.Unlock()
	if p, ok := turnPatterns[l.Code]; ok {
		return p
	}
	words := make([]string, len(l.turnWords))
	for i, w := range l.turnWords {
		words[i] = regexp.QuoteMeta(w)
	}
	// (?i) for the words; \b does not sit well after a letter like ę, so the
	// number is required to follow whitespace and end the word.
	p := regexp.MustCompile(`(?i)(?:^|[^\pL])(?:` + strings.Join(words, "|") + `)\s+(\d+)\b`)
	turnPatterns[l.Code] = p
	return p
}

// languageLine is the sentence added to every prompt when the coach writes in
// something other than English. Numbers as words in that language matter
// because the lines are spoken.
func languageLine(code string) string {
	l := languageOf(code)
	if l.Code == DefaultLanguage {
		return ""
	}
	return "Write everything in " + l.Name + " — every line, the debrief, the reading. Numbers as words in " +
		l.Name + ", since the lines are spoken. Name a corner the way a " + l.Name + "-speaking driver would."
}
