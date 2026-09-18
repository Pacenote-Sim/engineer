// Package engineer is the coaching plugin: a race engineer for a team's drivers.
//
// In practice it turns the lap just driven into one line per corner, spoken
// just before that corner on the next lap. In a race it is the radio: twelve
// words when something changed, silence otherwise. After a stint it writes the
// debrief and, when the car can be changed, up to three setup changes with the
// measurement behind each. For the team it reads a driver's debriefs as one
// story and says what keeps costing them time.
//
// The client that measured the lap posts what it measured to this plugin's own
// address; everything else arrives as the server's events. The server is not
// edited for any of it.
//
// The rule the whole thing is built on is worth repeating here: the model
// narrates, it never computes. Every number that reaches a driver was measured
// exactly before this plugin saw it, and everything the prompt asks for about a
// spoken line is checked again in code after the model answers.
package engineer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pacenote-sim/plugin"

	"github.com/pacenote-sim/engineer/internal/anthropic"
)

// debriefTokens bounds a debrief: a summary, three findings and, when the car
// can be changed, three changes. It is here rather than in the settings because
// an operator raising it would not get a better debrief — they would get a
// longer one.
const debriefTokens = 1500

// CueKind is which word limit a spoken line gets. The two differ because one is
// heard while racing somebody.
type CueKind string

// The two kinds.
const (
	CueRace     CueKind = "cue.race"
	CueTraining CueKind = "cue.training"
)

// jobDebrief is what a debrief is metered as.
const jobDebrief = "debrief"

// Engineer implements the plugin contract.
type Engineer struct {
	// Store is where what was written is kept. A nil store is a plugin running
	// without a database, which is not an error: it answers everything it can
	// and does not keep what it wrote.
	Store *Store
	// Log receives this plugin's own lines. It goes to standard error, which
	// the host captures, scrubs and shows in the panel.
	Log *slog.Logger
	// BaseURL overrides where the vendor is, for tests.
	BaseURL string
	// now is the clock, injectable for tests.
	now func() time.Time

	// The prompt parts an operator has written, cached.
	//
	// A lap's cues are on the critical path — the client is waiting with the
	// host's deadline — so this is not a database round trip per lap. It is
	// read once and dropped when a part is saved, which happens in this
	// process because the page that saves it is served by this process.
	mu      sync.RWMutex
	written map[Part]Written
	loaded  bool

	// races is what is remembered about race stints in progress, for the radio.
	races races

	// host is how this plugin asks others — voice, for the lines' audio. Set
	// once by Connected; nil is a host that offered nothing.
	hostMu sync.RWMutex
	host   plugin.Host
}

// This plugin asks voice for audio; the host connects it only if it says so.
var _ plugin.Asker = (*Engineer)(nil)

// prompt is every part, resolved: what an operator wrote where they wrote
// something, and what the binary ships everywhere else.
//
// The second return identifies the whole set on an answer. It is the built-in
// version alone when nothing has been changed, and that version plus a digest
// over every part that has been. One digest rather than eight, because what
// produced an answer is the combination.
func (e *Engineer) prompt(ctx context.Context) (map[Part]string, string) {
	e.mu.RLock()
	written, loaded := e.written, e.loaded
	e.mu.RUnlock()

	if !loaded && e.Store != nil {
		got, err := e.Store.Prompts(ctx)
		if err != nil {
			// The binary's own text is the answer when the written one cannot
			// be read. A coach that goes quiet because a table is unreachable
			// is worse than a coach that sounds generic.
			e.Log.LogAttrs(ctx, slog.LevelWarn, "the written prompt could not be read, using the built-in one",
				slog.String("error", err.Error()))
			return builtInParts(), PromptVersion
		}
		e.mu.Lock()
		e.written, e.loaded = got, true
		e.mu.Unlock()
		written = got
	}

	out := builtInParts()
	var digest []string
	for _, info := range Parts() {
		w, ok := written[info.Part]
		if !ok || strings.TrimSpace(w.Markdown) == "" {
			continue
		}
		out[info.Part] = w.Markdown
		digest = append(digest, string(info.Part)+":"+w.SHA256)
	}
	if len(digest) == 0 {
		return out, PromptVersion
	}
	sum := sha256.Sum256([]byte(strings.Join(digest, "\n")))
	return out, PromptVersion + "+" + hex.EncodeToString(sum[:])[:8]
}

// builtInParts is what the binary ships, every part of it.
func builtInParts() map[Part]string {
	parts := Parts()
	out := make(map[Part]string, len(parts))
	for _, info := range parts {
		out[info.Part] = info.BuiltIn
	}
	return out
}

// forget drops the cached prompt, so the next call reads what was just saved.
func (e *Engineer) forget() {
	e.mu.Lock()
	e.written, e.loaded = nil, false
	e.mu.Unlock()
}

// New builds the plugin.
func New(store *Store, log *slog.Logger) *Engineer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engineer{Store: store, Log: log, now: time.Now}
}

// Settings declares what the operator has to configure.
func (e *Engineer) Settings(context.Context) ([]plugin.Setting, error) { return Settings(), nil }

// Notify is told something happened.
//
// A completed lap is the radio in a race and nothing anywhere else: the corner
// cues come through the client's own report, where somebody is holding a
// deadline. A finished stint is the debrief, when the operator asked for one.
func (e *Engineer) Notify(ctx context.Context, ev plugin.Event) (plugin.Usage, error) {
	cfg := configOf(ev.Settings, ev.Secrets)
	switch ev.Kind {
	case plugin.EventLapCompleted:
		if ev.Lap == nil {
			return plugin.Usage{}, nil
		}
		return e.lapCompleted(ctx, cfg, ev)
	case plugin.EventStintFinished:
		e.races.forget(ev.Session.StintID)
		if !cfg.debriefs {
			return plugin.Usage{}, nil
		}
		if !cfg.configured() {
			return plugin.Usage{}, plugin.ErrNotConfigured
		}
		if ev.Stint == nil {
			return plugin.Usage{}, nil
		}
		return e.stintFinished(ctx, cfg, ev)
	default:
		return plugin.Usage{}, nil
	}
}

// stintFinished writes the debrief and files it.
func (e *Engineer) stintFinished(ctx context.Context, cfg config, ev plugin.Event) (plugin.Usage, error) {
	// What the laps noted about the car while the setup was open, so the setup
	// advice is the end of a conversation rather than a guess from one sheet.
	var notes []string
	if e.Store != nil {
		var err error
		if notes, err = e.Store.NotesFor(ctx, ev.Driver.Slug, ev.Session.StintID); err != nil {
			e.Log.LogAttrs(ctx, slog.LevelWarn, "the notes about the car could not be read",
				slog.String("stint", ev.Session.StintID), slog.String("reason", err.Error()))
		}
	}
	// Setup advice needs a setup sheet. No sheet — a fixed setup, or a
	// simulator that publishes none — is nothing to advise on.
	withSetup := cfg.setup && setupOf(ev.Stint.Setup) != nil

	out, use, promptVersion, err := e.debrief(ctx, cfg, ev.Stint, notes, withSetup)
	if err != nil {
		return use, err
	}
	if e.Store == nil {
		return use, nil
	}
	// Storing is best effort and deliberately after the answer is complete: a
	// debrief that was written and could not be filed is still a debrief, and
	// failing the event would make the host log it as the plugin being broken.
	if err := e.Store.SaveDebrief(ctx, Debrief{
		StintID:    ev.Session.StintID,
		DriverSlug: ev.Driver.Slug,
		DriverName: ev.Driver.Name,
		Sim:        ev.Session.Sim,
		Track:      ev.Session.Track,
		Car:        ev.Session.Car,
		Summary:    out.Summary,
		Findings:   out.Findings,
		Setup:      out.Setup,
		Model:      cfg.model,
		Prompt:     promptVersion,
		WrittenAt:  e.clock(),
	}); err != nil {
		e.Log.LogAttrs(ctx, slog.LevelWarn, "the debrief was written but could not be filed",
			slog.String("driver", ev.Driver.Slug), slog.String("reason", err.Error()))
	}
	// The retention setting runs here, once per stint, which is often enough
	// for something measured in seasons.
	if n, err := e.Store.Prune(ctx, cfg.keepDays); err != nil {
		e.Log.LogAttrs(ctx, slog.LevelWarn, "old debriefs and cues could not be removed", slog.String("reason", err.Error()))
	} else if n > 0 {
		e.Log.LogAttrs(ctx, slog.LevelInfo, "old debriefs and cues were removed", slog.Int64("rows", n))
	}
	return use, nil
}

// debriefOut is what the debrief job produces, before it is rendered.
type debriefOut struct {
	Summary  string        `json:"summary"`
	Findings []Finding     `json:"findings"`
	Setup    []SetupChange `json:"setup_changes,omitempty"`
}

// Text renders a debrief into the prose a driver reads. It is built here rather
// than asked for as prose so that the parts can also be stored separately, and
// so the shape is the same every time.
func (d debriefOut) Text() string {
	var b strings.Builder
	b.WriteString(d.Summary)
	for _, f := range d.Findings {
		fmt.Fprintf(&b, "\n\n%s. %s %s", f.Area, f.Observation, f.Drill)
	}
	for _, s := range d.Setup {
		fmt.Fprintf(&b, "\n\nSetup — %s: %s. %s", s.Setting, s.fromTo(), s.Because)
	}
	return b.String()
}

// debrief writes the few hundred words after a session, and the setup changes
// when there is a sheet to change.
func (e *Engineer) debrief(ctx context.Context, cfg config, stint *plugin.StintFacts, notes []string, withSetup bool) (debriefOut, plugin.Usage, string, error) {
	if stint == nil {
		return debriefOut{}, plugin.Usage{}, PromptVersion, plugin.ErrNoAnswer
	}

	parts, promptVersion := e.prompt(ctx)
	system := parts[PartPersona] + "\n\n" + parts[PartDebrief]
	facts := stintFacts(stint) + "\n" + setupFacts(stint.Setup)
	if withSetup {
		system += "\n\n" + parts[PartSetup]
		facts += "\n" + notesFacts(notes)
	}
	system = inLanguage(system, cfg.language)

	raw, spent, err := e.client(cfg).Ask(ctx, system, facts, debriefTool(withSetup), debriefTokens)
	use := usageOf(jobDebrief, cfg.model, spent)
	if err != nil {
		return debriefOut{}, use, promptVersion, e.vendorError(ctx, jobDebrief, err)
	}

	var out debriefOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return debriefOut{}, use, promptVersion, fmt.Errorf("%w: the debrief could not be read: %w", plugin.ErrNoAnswer, err)
	}
	out.Summary = strings.TrimSpace(out.Summary)
	if withSetup {
		out.Setup = cleanChanges(out.Setup)
	} else {
		// Nothing about the car was asked for, so nothing about the car is
		// kept, whatever came back.
		out.Setup = nil
	}
	if out.Summary == "" && len(out.Findings) == 0 && len(out.Setup) == 0 {
		return debriefOut{}, use, promptVersion, plugin.ErrNoAnswer
	}
	return out, use, promptVersion, nil
}

// cleanChanges keeps the changes that are ones: a control, a value to try, and
// at most three of them.
func cleanChanges(in []SetupChange) []SetupChange {
	out := make([]SetupChange, 0, MaxSetupChanges)
	for _, s := range in {
		s.Setting, s.From, s.To, s.Because = strings.TrimSpace(s.Setting), strings.TrimSpace(s.From),
			strings.TrimSpace(s.To), strings.TrimSpace(s.Because)
		if s.Setting == "" || s.To == "" {
			continue
		}
		out = append(out, s)
		if len(out) == MaxSetupChanges {
			break
		}
	}
	return out
}

// client is the vendor client for one call, holding the operator's key for no
// longer than the call takes.
func (e *Engineer) client(cfg config) anthropic.Client {
	return anthropic.Client{
		Key:     cfg.key.Value(),
		Model:   cfg.model,
		BaseURL: e.BaseURL,
	}
}

// vendorError turns a refusal into something the caller and the operator can
// each use. The caller gets [plugin.ErrNoAnswer] — there is nothing to say, and
// at the moment a driver is waiting nothing else matters. The operator gets a
// log line that separates "your key is wrong", which they must fix, from "you
// are going too fast", which will pass.
func (e *Engineer) vendorError(ctx context.Context, job string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		// Missing the deadline is the one unforgivable failure, and it is not
		// worth a log line every lap: the host already knows.
		return fmt.Errorf("%w: %w", plugin.ErrNoAnswer, err)
	case anthropic.Retryable(err):
		e.Log.LogAttrs(ctx, slog.LevelWarn, "the vendor was busy, so nothing was said",
			slog.String("job", job), slog.String("reason", err.Error()))
	case errors.Is(err, anthropic.ErrRefused):
		e.Log.LogAttrs(ctx, slog.LevelError, "the vendor refused, and this will keep happening until it is fixed",
			slog.String("job", job), slog.String("reason", err.Error()))
	default:
		e.Log.LogAttrs(ctx, slog.LevelWarn, "the coach could not answer",
			slog.String("job", job), slog.String("reason", err.Error()))
	}
	return fmt.Errorf("%w: %w", plugin.ErrNoAnswer, err)
}

// usageOf turns what the vendor counted into what the core meters. The job and
// the model are named because that is what an operator reads when they ask where
// the money went, and "call" is not an answer to that question.
func usageOf(job, model string, u anthropic.Usage) plugin.Usage {
	return plugin.Usage{
		Job:          job,
		Model:        model,
		InputTokens:  u.Input,
		OutputTokens: u.Output,
	}
}

func (e *Engineer) clock() time.Time {
	if e.now == nil {
		return time.Now()
	}
	return e.now()
}
