package engineer

import "github.com/pacenote-sim/plugin"

// What the operator fills in. Every one of them is a decision only they can
// make: whose key is being spent, how much a sentence is worth, which of the
// jobs that cost money are wanted, and how long what was written is kept.
//
// Everything else about how this plugin behaves is in the prompts and the
// validator, where it belongs. A setting that let an operator loosen the cue
// rules would be a setting that lets them make the coach worse, and the rules
// are the reason the coach is worth listening to.
const (
	SettingAPIKey   = "api_key"
	SettingModel    = "model"
	SettingDebriefs = "debriefs"
	SettingRacePace = "racepace"
	SettingSetup    = "setup"
	SettingSpeak    = "speak"
	SettingLanguage = "language"
	SettingKeepDays = "keep_days"
)

// The models this plugin offers, and what each is for. They are a fixed list
// rather than a text field: a model id typed by hand is a plugin that fails at
// the vendor with a message the operator cannot act on, and a list is also where
// the cost difference gets said out loud.
const (
	ModelOpus   = "claude-opus-5"
	ModelSonnet = "claude-sonnet-5"
	ModelHaiku  = "claude-haiku-4-5-20251001"
)

// DefaultKeepDays is how long a debrief is kept. A season is the unit a driver
// thinks in, and a debrief older than that is not something anybody reads — but
// it is the operator's disk and their decision, so it is a setting.
const DefaultKeepDays = 180

// Settings is what the panel renders. It does not depend on anything being
// configured, because a fresh installation has nothing configured and the form
// is how it stops being fresh.
func Settings() []plugin.Setting {
	return []plugin.Setting{
		{
			Name:     SettingAPIKey,
			Label:    "Anthropic key",
			Help:     "Yours, from console.anthropic.com. It is sealed with this server's data key and lent to this plugin one call at a time. Without it the coach still works — the client speaks its own cues — it is just less good.",
			Kind:     plugin.KindSecret,
			Required: true,
		},
		{
			Name:  SettingModel,
			Label: "Model",
			Help:  "What writes the cues. A cue is one sentence, so the cheap model is closer to the expensive one here than you would expect — try the middle one first.",
			Kind:  plugin.KindChoice,
			Choices: []plugin.Choice{
				{Value: ModelSonnet, Label: "Claude Sonnet 5", Note: "most of the quality, about a third of the cost"},
				{Value: ModelOpus, Label: "Claude Opus 5", Note: "the most capable, and the most expensive per token"},
				{Value: ModelHaiku, Label: "Claude Haiku 4.5", Note: "fastest and cheapest; good enough for cues, thin for debriefs"},
			},
			Default: ModelSonnet,
		},
		{
			Name:    SettingLanguage,
			Label:   "Language",
			Help:    "What the coach writes in — the lines, the debrief, the reading. It tells the voice plugin too, so the words and the voice agree.",
			Kind:    plugin.KindChoice,
			Choices: languageChoices(),
			Default: DefaultLanguage,
		},
		{
			Name:    SettingDebriefs,
			Label:   "Write a debrief after every stint",
			Help:    "A few hundred words a driver reads afterwards. It is the most expensive thing this plugin does and the only one nobody is waiting on, so it is the first to turn off if the bill matters.",
			Kind:    plugin.KindBool,
			Default: "true",
		},
		{
			Name:    SettingRacePace,
			Label:   "Speak on the radio in a race",
			Help:    "At most twelve words, and only when something changed: a position, a car closing to within a second, a personal best, a lap half a second off the one before. Silence otherwise.",
			Kind:    plugin.KindBool,
			Default: "true",
		},
		{
			Name:    SettingSetup,
			Label:   "Suggest setup changes",
			Help:    "When the session lets the car be changed and the simulator publishes the sheet: the coach notes what the car is doing lap by lap and, with the debrief, suggests up to three changes with the measurement behind each. Nothing on a fixed setup.",
			Kind:    plugin.KindBool,
			Default: "true",
		},
		{
			Name:    SettingSpeak,
			Label:   "Speak the lines",
			Help:    "When a plugin called voice is installed, every line is sent to it and the client gets the audio with the words. Voice's own settings say which voice and what it costs; off here means text only.",
			Kind:    plugin.KindBool,
			Default: "true",
		},
		{
			Name:        SettingKeepDays,
			Label:       "Keep debriefs and cues for",
			Help:        "Days. A stint's worth is a few kilobytes and a season is about 180 days. Zero keeps them for ever.",
			Kind:        plugin.KindNumber,
			Default:     "180",
			Placeholder: "180",
		},
	}
}

// config is the settings as this plugin uses them, read once per call from what
// the host lends it.
type config struct {
	key      plugin.Secret
	model    string
	debriefs bool
	racePace bool
	setup    bool
	speak    bool
	language string
	keepDays int
}

// configOf reads a call's settings. A value the host did not send falls back to
// the declared default, which [plugin.ValidateValues] has already applied — so
// anything missing here is a host that sent nothing, and the defaults are
// repeated rather than left as zero.
func configOf(values plugin.Values, secrets plugin.Secrets) config {
	c := config{
		key:      secrets[SettingAPIKey],
		model:    values.String(SettingModel),
		debriefs: values.Bool(SettingDebriefs),
		racePace: values.Bool(SettingRacePace),
		setup:    values.Bool(SettingSetup),
		speak:    values.Bool(SettingSpeak),
		language: languageOf(values.String(SettingLanguage)).Code,
	}
	if days, ok := values.Int(SettingKeepDays); ok {
		c.keepDays = days
	} else {
		c.keepDays = DefaultKeepDays
	}
	if c.model == "" {
		c.model = ModelSonnet
	}
	return c
}

// configured reports whether this plugin can call anything at all. A missing key
// is the commonest state of a freshly installed plugin, and it is not an error:
// it is [plugin.ErrNotConfigured], which the caller falls back from.
func (c config) configured() bool { return !c.key.Empty() }

// languageChoices is the languages offered, as the panel renders them.
func languageChoices() []plugin.Choice {
	out := make([]plugin.Choice, 0, len(languages))
	for _, l := range languages {
		out = append(out, plugin.Choice{Value: l.Code, Label: l.Name})
	}
	return out
}
