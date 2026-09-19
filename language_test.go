package engineer

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// The coach writes in the operator's language, and the validator reads it.

func TestACornerIsNamedInTheLanguageOfTheLine(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Spanish: "Curva 5" is the corner; "Turn 5" still is, because that is
	// what most circuits call them.
	r.NoError(ValidateCornerLine("es", CueTraining, "Frena treinta metros más tarde en la Curva 5.", 5))
	r.NoError(ValidateCornerLine("es", CueTraining, "Frena más tarde en Turn 5.", 5))
	err := ValidateCornerLine("es", CueTraining, "Frena más tarde en la curva 9.", 5)
	r.Error(err, "an invented corner in Spanish got through")
	r.Contains(err.Error(), "names turn 9")

	// A number that is not a corner is not caught by the corner rule: "curva"
	// is the word, and "de 5 grados" is not a corner.
	r.NoError(ValidateCue("es", CueTraining, "Suelta el freno hasta el cinco por ciento.", cornersNumbered(1)))

	// Every language offered finds its own word, and English in all of them.
	for _, l := range languages {
		for _, w := range l.turnWords {
			line := "Something about " + w + " 4 here."
			r.NoErrorf(ValidateCue(l.Code, CueTraining, line, cornersNumbered(4)), "%s: %q not found", l.Code, line)
			r.Errorf(ValidateCue(l.Code, CueTraining, line, cornersNumbered(3)), "%s: %q let an invented corner through", l.Code, line)
		}
	}
	// Polish, with the letter the regexp has to be careful around.
	r.Error(ValidateCue("pl", CueTraining, "Hamuj później w zakręt 7.", cornersNumbered(4)))
	r.NoError(ValidateCue("pl", CueTraining, "Hamuj później w zakręt 4.", cornersNumbered(4)))

	// A language nobody offered is English.
	r.Equal("en", languageOf("xx").Code)
	r.Equal("Spanish", languageOf("es").Name)
	r.Empty(languageLine("en"), "English needs no telling")
	r.Contains(languageLine("de"), "Write everything in German")
}

func TestTheCoachIsToldWhatLanguageToWriteIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Frena treinta metros más tarde en la Curva 1."}}})
	host := &fakeHost{audio: []byte("audio")}
	e.Connected(host)

	res := postReport(t, e, aReport(), func(q *plugin.HTTPRequest) { q.Settings[SettingLanguage] = "es" })
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	r.Contains(*asked, "Write everything in Spanish")
	r.Len(cuesOf(t, res).Cues, 1, "a Spanish line naming its own corner was refused")

	// And voice is told which language the words are in.
	r.Equal([]string{"Frena treinta metros más tarde en la Curva 1."}, host.said())
	r.Contains(host.payloads[0], `"language":"es"`)

	// English is the default and says nothing extra.
	e2, _, asked2 := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Brake later."}}})
	_ = postReport(t, e2, aReport())
	r.NotContains(*asked2, "Write everything in")

	// A stint's debrief and the reading are written in it too.
	ev := stintFinished()
	ev.Settings[SettingLanguage] = "es"
	e3, _, asked3 := vendorSaying(t, debriefOut{Summary: "Buen ritmo."})
	_, err := e3.Notify(t.Context(), ev)
	r.NoError(err)
	r.Contains(*asked3, "Write everything in Spanish")
}

func TestALineNamesItsCornerFirst(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	r.Equal("Turn 4", TurnName("en", 4))
	r.Equal("Curva 4", TurnName("es", 4))
	r.Equal("Kurve 12", TurnName("de", 12))
	r.Equal("Zakręt 2", TurnName("pl", 2))
	r.Equal("Turn 4", TurnName("xx", 4), "an unknown language is English")

	r.Equal("Brake later into Turn 4.", EnsureTurnNamed("en", "Brake later into Turn 4.", 4), "already named: untouched")
	r.Equal("Turn 4. Brake twenty metres later.", EnsureTurnNamed("en", "Brake twenty metres later.", 4))
	r.Equal("Curva 5. Frena más tarde.", EnsureTurnNamed("es", "  Frena más tarde.  ", 5))
	r.Equal("Frena más tarde en la curva 5.", EnsureTurnNamed("es", "Frena más tarde en la curva 5.", 5), "the local word counts")
	r.Equal("Curva 5. Frena como en la curva 4.", EnsureTurnNamed("es", "Frena como en la curva 4.", 5), "another corner's name is not this one's")
	r.Empty(EnsureTurnNamed("en", "   ", 4))
	r.NoError(ValidateCornerLine("en", CueTraining, EnsureTurnNamed("en", "Brake later.", 4), 4))
}

// Joined corners are named as a list, and every number in the list counts as
// a corner named: "Turns 3 and 9" names 9, and 9 is checked like any other.
func TestAListOfCornersNamesEachOfThem(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal([]string{"3", "4"}, turnsNamed("en", "Turns 3 and 4, brake once and carry the speed."))
	r.Equal([]string{"3", "4", "5"}, turnsNamed("es", "Curvas 3, 4 y 5: frena una vez."))
	r.Equal([]string{"3", "4"}, turnsNamed("de", "Kurven 3 und 4 zusammen nehmen."))
	r.Equal([]string{"3"}, turnsNamed("en", "Turn 3 and then 4 metres later brake."), "a number after another word is not a corner")
	r.Equal([]string{"3", "9"}, turnsNamed("en", "Turn 3, then Turn 9."))
	r.Empty(turnsNamed("en", "Brake later."))

	r.Equal("Turns 3 and 4, brake once.", EnsureTurnNamed("en", "Turns 3 and 4, brake once.", 4), "named in a list")
	r.Equal("Turn 3. Turn 4, brake later.", EnsureTurnNamed("en", "Turn 4, brake later.", 3),
		"a joined line naming only the second corner gets the first put in front")
}

// The coach writes numbers as words because the lines are spoken, so "Curva
// uno" names Turn 1 as surely as "Curva 1" does — and is not named again in
// front, which is what a driver heard as "Curva 1. Curva uno".
func TestACornerNumberedInWordsIsNamed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Curva uno, frena más tarde.", EnsureTurnNamed("es", "Curva uno, frena más tarde.", 1))
	r.Equal("Turn one, brake later.", EnsureTurnNamed("en", "Turn one, brake later.", 1))
	r.Equal([]string{"1"}, turnsNamed("es", "Curva uno, frena más tarde."))
	r.Equal([]string{"3", "4", "5"}, turnsNamed("es", "Curvas tres, cuatro y cinco: una sola frenada."))
	r.Equal([]string{"21"}, turnsNamed("es", "Curva veintiuno, entra más rápido."))
	r.Equal([]string{"22"}, turnsNamed("fr", "Virage vingt-deux, freine plus tard."), "the longer spelling wins over vingt")
	r.Equal([]string{"21"}, turnsNamed("fr", "Virage vingt et un, freine plus tard."))
	r.Equal([]string{"17"}, turnsNamed("pt", "Curva dezassete, trava mais tarde."))
	r.Equal([]string{"12"}, turnsNamed("de", "Kurve zwölf, später bremsen."))
	r.Equal([]string{"25"}, turnsNamed("de", "Kurve fünfundzwanzig, später bremsen."))
	r.Equal([]string{"23"}, turnsNamed("nl", "Bocht drieëntwintig, later remmen."))
	r.Equal([]string{"15"}, turnsNamed("pl", "Zakręt piętnaście, hamuj później."))
	r.Equal([]string{"1"}, turnsNamed("es", "Curva one, frena."), "English words are understood everywhere")
	r.Equal([]string{"4"}, turnsNamed("en", "Turn 04, brake."), "leading zeros are not a different corner")
	r.Empty(turnsNamed("en", "Turn onesie, brake."), "a spelling that runs on is not a number")
	r.Equal([]string{"3"}, turnsNamed("en", "Turns 3 and onesie."), "nor is one in a list")
	r.Equal([]string{"3", "9"}, turnsNamed("en", "Turn 3 Turn 9"), "back to back without punctuation")

	// The validator sees the word the same way: naming another corner in
	// words is caught, and every language builds.
	err := ValidateCornerLine("es", CueTraining, "Frena más tarde en la curva nueve.", 5)
	r.Error(err)
	r.Contains(err.Error(), "names turn 9")
	for _, l := range languages {
		r.NotNil(matcherFor(l.Code))
		r.Len(numberWords[l.Code], len(numberWords[l.Code]), l.Code)
	}
	r.Equal(30, numberWords["it"]["trenta"])
	r.Panics(func() { words("uno", "", "x") })
	r.Panics(func() { words("uno", "", "x=40") })
}

// Every language says the things a coach says in its own idiom. A model told
// only to write in Spanish translates English into Spanish — "recoge el gas"
// for lifting off, which no one says on the radio — so it is given the words.
func TestTheCoachIsGivenTheWordsDriversUse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, l := range languages {
		line := speaksLine(l.Code)
		r.Contains(line, "race engineer on the radio", l.Code)
		r.Contains(line, l.Name, l.Code)
		r.NotEmpty(l.speaks, l.Code)
		r.GreaterOrEqual(strings.Count(l.speaks, ",")+1, 10, "%s is given too few words to work with", l.Code)
	}
	// The vocabulary is the language's own, not a translation of the English.
	r.Contains(speaksLine("es"), "apura la frenada")
	r.Contains(speaksLine("es"), "el vértice")
	r.NotContains(speaksLine("es"), "recoge el gas")
	r.Contains(speaksLine("it"), "la staccata")
	r.Contains(speaksLine("fr"), "la corde")
	r.Contains(speaksLine("de"), "der Scheitelpunkt")
	r.Contains(speaksLine("pl"), "wierzchołek")
	r.Contains(speaksLine("nl"), "onderstuur")
	r.Contains(speaksLine("pt"), "o ponto de travagem")
	// English is told too: it is a register, not a translation.
	r.Contains(speaksLine("en"), "trail the brake")
	// A language nobody offers falls back to English rather than to nothing.
	r.Equal(speaksLine("en"), speaksLine("kl"))
}

// A model writing Spanish that reaches for the English word for a corner —
// "Turn uno", which is what the facts it reads call them — has its line given
// the word the driver expects.
func TestACornerIsNamedInTheLocalWord(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Curva 1, mismo problema, abre gas antes.",
		EnsureTurnNamed("es", "Turn uno, mismo problema, abre gas antes.", 1))
	r.Equal("Curva 4, sales del freno bien.", EnsureTurnNamed("es", "Turn 4: sales del freno bien.", 4))
	r.Equal("Kurve 9, später bremsen.", EnsureTurnNamed("de", "Turn nine — später bremsen.", 9))
	// Its own word is left alone, digits or words.
	r.Equal("Curva uno, mismo problema.", EnsureTurnNamed("es", "Curva uno, mismo problema.", 1))
	r.Equal("Curva 1, mismo problema.", EnsureTurnNamed("es", "Curva 1, mismo problema.", 1))
	r.Equal("Turn 4, brake later.", EnsureTurnNamed("en", "Turn 4, brake later.", 4))
	// The wrong corner is not renamed into the right one; it is caught.
	r.Error(ValidateCornerLine("es", CueTraining, "Turn nueve, frena antes.", 4))
	// And the coach is told which word to use in the first place.
	r.Contains(languageLine("es"), "Curva 4 and not Turn 4")
	r.Contains(languageLine("pl"), "Zakręt 4")
}
