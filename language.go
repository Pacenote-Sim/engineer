package engineer

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
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
	// speaks is how a driver in this language says the things a coach says to
	// them. A model asked only to write in Spanish writes English translated
	// into Spanish — "recoge el gas" for lift off — which is not what anyone
	// says on the radio. These are the words the line is built from.
	speaks string
}

// languages is the list offered, English first.
var languages = []language{
	{
		Code: "en", Name: "English", turnWords: []string{"turn", "turns"},
		speaks: "brake later, carry more speed, trail the brake, release the brake, get on the power earlier, " +
			"short-shift, turn-in, apex, clip the apex, open the exit, understeer, oversteer, the kerb, " +
			"the slipstream, the braking zone",
	},
	{
		Code: "es", Name: "Spanish", turnWords: []string{"curva", "curvas", "turn", "turns"},
		speaks: "frena más tarde, apura la frenada, el punto de frenada, suelta el freno, levanta el pie, " +
			"abre gas antes, pisa antes, la trazada, el vértice (el ápice), la entrada, la salida, " +
			"subviraje, sobreviraje, el piano, el rebufo",
	},
	{
		Code: "de", Name: "German", turnWords: []string{"kurve", "kurven", "turn", "turns"},
		speaks: "später bremsen, der Bremspunkt, die Bremse lösen, vom Gas gehen, früher ans Gas, die Ideallinie, " +
			"der Scheitelpunkt, der Kurveneingang, der Kurvenausgang, Untersteuern, Übersteuern, der Randstein, " +
			"der Windschatten",
	},
	{
		Code: "fr", Name: "French", turnWords: []string{"virage", "virages", "turn", "turns"},
		speaks: "freine plus tard, le point de freinage, relâche les freins, lève le pied, remets les gaz plus tôt, " +
			"la trajectoire, la corde, l'entrée, la sortie, le sous-virage, le survirage, le vibreur, l'aspiration",
	},
	{
		Code: "it", Name: "Italian", turnWords: []string{"curva", "curve", "turn", "turns"},
		speaks: "frena più tardi, ritarda la staccata, il punto di staccata, rilascia il freno, alza il piede, " +
			"dai gas prima, la traiettoria, la corda, l'ingresso, l'uscita, il sottosterzo, il soprasterzo, " +
			"il cordolo, la scia",
	},
	{
		Code: "pt", Name: "Portuguese", turnWords: []string{"curva", "curvas", "turn", "turns"},
		speaks: "trava mais tarde (freia mais tarde no Brasil), o ponto de travagem, alivia o travão (o freio), " +
			"levanta o pé, acelera mais cedo, a trajetória, o vértice, a entrada, a saída, subviragem, " +
			"sobreviragem, a corda",
	},
	{
		Code: "nl", Name: "Dutch", turnWords: []string{"bocht", "bochten", "turn", "turns"},
		speaks: "rem later, het rempunt, laat de rem los, van het gas, eerder op het gas, de lijn, de apex, " +
			"de ingang, de uitgang, onderstuur, overstuur, de kerb, de slipstream",
	},
	{
		Code: "pl", Name: "Polish", turnWords: []string{"zakręt", "zakręty", "zakret", "zakrety", "turn", "turns"},
		speaks: "hamuj później, punkt hamowania, puść hamulec, zdejmij gaz, wcześniej dodaj gazu, tor jazdy, " +
			"wierzchołek, wjazd, wyjazd, podsterowność, nadsterowność, krawężnik",
	},
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

// turnMatcher finds the corners a line names in one language: the turn word
// followed by a number, as digits or as a word, and the further numbers of a
// list after it — "Turns 3 and 4", "curvas tres, cuatro y cinco".
type turnMatcher struct {
	turn, list, lead, open *regexp.Regexp
	value                  map[string]int
	// word is what a corner is called in this language, lowercase.
	word string
}

var (
	turnMatchers   = map[string]*turnMatcher{}
	turnMatchersMu sync.Mutex
)

// listWords join the numbers of a list: every language's "and" and "or",
// short enough to keep together rather than per language.
const listWords = `and|or|y|o|u|e|ou|et|und|oder|en|of|i|oraz|lub`

// matcherFor is the matcher for a language, compiled once.
func matcherFor(code string) *turnMatcher {
	l := languageOf(code)
	turnMatchersMu.Lock()
	defer turnMatchersMu.Unlock()
	if m, ok := turnMatchers[l.Code]; ok {
		return m
	}
	value := make(map[string]int, 128)
	for w, n := range numberWords[DefaultLanguage] {
		value[w] = n
	}
	for w, n := range numberWords[l.Code] {
		value[w] = n
	}
	// Longest spelling first: the alternation takes the first that fits, and
	// "vingt" must not win over "vingt-deux".
	spellings := make([]string, 0, len(value))
	for w := range value {
		spellings = append(spellings, regexp.QuoteMeta(w))
	}
	sort.Slice(spellings, func(i, j int) bool {
		if len(spellings[i]) != len(spellings[j]) {
			return len(spellings[i]) > len(spellings[j])
		}
		return spellings[i] < spellings[j]
	})
	number := `(\d+|` + strings.Join(spellings, "|") + `)`
	turnWords := make([]string, len(l.turnWords))
	for i, w := range l.turnWords {
		turnWords[i] = regexp.QuoteMeta(w)
	}
	// (?i) for the words. A letter right after the number is checked in code
	// rather than with \b, which does not sit well after a letter like ę.
	m := &turnMatcher{
		turn: regexp.MustCompile(`(?i)(?:^|[^\pL])(?:` + strings.Join(turnWords, "|") + `)\s+` + number),
		list: regexp.MustCompile(`(?i)^(?:\s*,|\s+(?:` + listWords + `))\s+` + number),
		// A line that opens with the number alone and then breaks — "Cuatro,
		// recoge el gas" — has named its corner as surely as "Curva 4"; the
		// break is what tells a corner from a distance, since "cuatro metros"
		// is four metres and not Turn 4.
		lead: regexp.MustCompile(`(?i)^\s*` + number + `\s*[,:;.\x{2013}\x{2014}-]\s*`),
		// A line that opens by naming its corner, whatever word it used for
		// one: "Turn uno" from a model writing Spanish is the English word
		// with a Spanish number, and is not what the driver should hear.
		open:  regexp.MustCompile(`(?i)^\s*(` + strings.Join(turnWords, "|") + `)\s+` + number + `\s*[,:;.\x{2013}\x{2014}-]?\s*`),
		value: value,
		word:  l.turnWords[0],
	}
	turnMatchers[l.Code] = m
	return m
}

// number is the corner number a match spelled, as digits, or "" when the
// spelling runs on into a longer word — "Turn onesie" names nothing.
func (m *turnMatcher) number(line string, from, to int) string {
	if to < len(line) {
		r, _ := utf8.DecodeRuneInString(line[to:])
		if unicode.IsLetter(r) {
			return ""
		}
	}
	word := line[from:to]
	if n, ok := m.value[strings.ToLower(word)]; ok {
		return strconv.Itoa(n)
	}
	return strings.TrimLeft(word, "0") // digits; "04" is turn 4
}

// turnsNamed is every corner a line names, in order and as digits, including
// the later numbers of a list: "Turns 3 and 4" names 3 and 4, and "Curva uno"
// names 1.
func turnsNamed(code, line string) []string {
	m := matcherFor(code)
	var out []string
	if idx := m.lead.FindStringSubmatchIndex(line); idx != nil {
		if n := m.number(line, idx[2], idx[3]); n != "" {
			out = append(out, n)
		}
	}
	for _, idx := range m.turn.FindAllStringSubmatchIndex(line, -1) {
		n := m.number(line, idx[2], idx[3])
		if n == "" {
			continue
		}
		out = append(out, n)
		at := idx[3]
		for {
			next := m.list.FindStringSubmatchIndex(line[at:])
			if next == nil {
				break
			}
			n := m.number(line, at+next[2], at+next[3])
			if n == "" {
				break
			}
			out = append(out, n)
			at += next[3]
		}
	}
	return out
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
		l.Name + ", since the lines are spoken. A corner is named with the " + l.Name + " word for one and its " +
		"number, " + TurnName(l.Code, 4) + " and not Turn 4; the facts you are given say Turn because they are " +
		"written in English, and you are not."
}

// speaksLine is the vocabulary a line is written from: what a race engineer
// says on the radio in that language. It is given in every language, English
// included, because the alternative is a line that reads like a manual.
func speaksLine(code string) string {
	l := languageOf(code)
	return "Speak as a race engineer on the radio, in the words drivers themselves use — not a word-for-word " +
		"translation of English, and not the language of a driving manual. The idiom of this trade in " +
		l.Name + ": " + l.speaks + ". Use these where they fit and stay in that register everywhere else."
}

// TurnName is how a corner is named in a language, capitalised for the start
// of a line: "Turn 4" in English, "Curva 4" in Spanish.
func TurnName(code string, turn int) string {
	word := languageOf(code).turnWords[0]
	r := []rune(word)
	r[0] = unicode.ToUpper(r[0])
	return string(r) + " " + strconv.Itoa(turn)
}

// EnsureTurnNamed is the line with its corner named at the front when the
// model left the name out. A driver acts on a corner number at once and
// cannot check it, so a line that names none is not left to be guessed at.
func EnsureTurnNamed(code, line string, turn int) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return line
	}
	m := matcherFor(code)
	// Named by the number alone — "Cuatro, recoge el gas" — it is given its
	// word, so the driver hears a corner and not a loose number.
	if idx := m.lead.FindStringSubmatchIndex(line); len(idx) >= 4 && m.number(line, idx[2], idx[3]) == strconv.Itoa(turn) {
		return TurnName(code, turn) + ", " + strings.TrimSpace(line[idx[1]:])
	}
	// Named with another language's word for a corner — "Turn uno" in a
	// Spanish line — it is given this language's.
	if idx := m.open.FindStringSubmatchIndex(line); len(idx) >= 6 && m.number(line, idx[4], idx[5]) == strconv.Itoa(turn) &&
		!strings.EqualFold(line[idx[2]:idx[3]], m.word) {
		return TurnName(code, turn) + ", " + strings.TrimSpace(line[idx[1]:])
	}
	// Named anywhere else, in a list or with its word: left as it was written.
	for _, named := range turnsNamed(code, line) {
		if named == strconv.Itoa(turn) {
			return line
		}
	}
	return TurnName(code, turn) + ". " + line
}
