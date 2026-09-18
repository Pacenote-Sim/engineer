package engineer

import (
	"net/http"
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
