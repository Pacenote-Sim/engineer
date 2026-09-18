package engineer

// What an operator may change about the coach, and what they may not.
//
// Everything here is prose the model is given. None of it is a guarantee: the
// word limits, the sentence count, how units are spelled and the rule that
// every turn named is a corner the detector found are enforced in validate.go
// afterwards, whatever the prompt asked for. Rewriting a part cannot make the
// coach say something the validator refuses — it can only produce cues that get
// discarded, which is why the page says so.
//
// The tool schemas are not here on purpose. They are the shape an answer comes
// back in, not a request about its tone: editing one does not change what the
// coach says, it stops the answer being readable at all.

// Part names one editable piece of the prompt.
type Part string

const (
	// PartPersona is who the coach is. It is on every call, which is what stops
	// it being a different person between a cue and the debrief that follows.
	PartPersona Part = "persona"
	// PartCue is how a lap's corner lines are asked for.
	PartCue Part = "cue"
	// PartRace is the one sentence added to the corner lines in a race.
	PartRace Part = "race"
	// PartPractice is the one sentence added everywhere else.
	PartPractice Part = "practice"
	// PartRacePace is the radio in a race.
	PartRacePace Part = "racepace"
	// PartSetup is about the car: the notes lap by lap and the changes after.
	PartSetup Part = "setup"
	// PartDebrief is how the written debrief is asked for.
	PartDebrief Part = "debrief"
	// PartProfile is the reading of a driver for the team.
	PartProfile Part = "profile"
)

// PartInfo is one part, for the page that edits it.
type PartInfo struct {
	Part Part
	// Title is what the page calls it, and About why an operator would change
	// it. Both are here rather than in the template so that adding a part is
	// one edit.
	Title string
	About string
	// BuiltIn is the text shipped in the binary, and what a part goes back to.
	BuiltIn string
	// Rows is how tall its box should be. A part that is one sentence and a
	// part that is four paragraphs do not want the same field.
	Rows int
}

// Parts is every editable part, in the order the page shows them: what the
// coach is, then each job it does.
func Parts() []PartInfo {
	return []PartInfo{
		{
			Part:  PartPersona,
			Title: "Who the coach is",
			About: "On every call. Your team, your class, how blunt you want it, what you call things.",
			//nolint:misspell // the built-in text is prose, not code.
			BuiltIn: persona,
			Rows:    14,
		},
		{
			Part:    PartCue,
			Title:   "Asking for the corner lines",
			About:   "What a spoken line has to be, one per corner. The validator enforces the limits whatever this says — asking for more than it allows produces lines that are thrown away.",
			BuiltIn: cueRules,
			Rows:    16,
		},
		{
			Part:    PartRace,
			Title:   "During a race",
			About:   "Added to the corner lines when the session is a race.",
			BuiltIn: raceLine,
			Rows:    3,
		},
		{
			Part:    PartPractice,
			Title:   "During practice",
			About:   "Added to the corner lines in practice, qualifying and testing.",
			BuiltIn: practiceLine,
			Rows:    3,
		},
		{
			Part:    PartRacePace,
			Title:   "On the radio in a race",
			About:   "What is said between corners when something changed: a position, a gap, the pace. Twelve words at most, enforced.",
			BuiltIn: racePaceRules,
			Rows:    9,
		},
		{
			Part:    PartSetup,
			Title:   "About the car",
			About:   "What to notice about the car lap by lap while the setup is open, and how to turn it into changes after the stint.",
			BuiltIn: setupRules,
			Rows:    12,
		},
		{
			Part:    PartDebrief,
			Title:   "Writing the debrief",
			About:   "What the driver reads after a session. The most expensive thing this plugin does.",
			BuiltIn: debriefRules,
			Rows:    10,
		},
		{
			Part:    PartProfile,
			Title:   "Reading a driver",
			About:   "How a driver's debriefs are read as one story, for the team's overview.",
			BuiltIn: profileRules,
			Rows:    8,
		},
	}
}

// builtIn is the shipped text for a part, and whether it is a part at all.
func builtIn(p Part) (string, bool) {
	for _, info := range Parts() {
		if info.Part == p {
			return info.BuiltIn, true
		}
	}
	return "", false
}
