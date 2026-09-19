package engineer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/pacenote-sim/plugin"
)

// The radio in a race.
//
// Nobody wants coaching in their ear while racing somebody. What they want is
// the engineer on the pit wall: where the lap sat, who is where, when something
// changed. The server already sends everything that takes — the lap against
// the reference, the position, the gaps either side, a personal best — on every
// completed lap, so this needs nothing from the client and nothing from the
// server that is not already there.
//
// It speaks only when something changed. Engineer works out what, from this lap
// and the one before it; the model puts it into twelve words. Silence is the
// usual answer and it is the right one: a line every lap is chatter, and
// chatter is what gets a radio turned down.

const (
	// jobRacePace is what a radio line is metered as.
	jobRacePace = "racepace"
	// raceTokens bounds the answer. Twelve words do not need more.
	raceTokens = 120
	// paceDropMs is how much slower than the lap before, against the same
	// reference, a clean lap has to be before it is worth a word. Half a
	// second is a driver who has lost something, not one who caught traffic.
	paceDropMs = 500
	// closeGapMs is a car within a second: a fight, which is worth a word when
	// it starts and not on every lap it continues.
	closeGapMs = 1000
	// maxRaces bounds what is remembered about stints in progress, for a host
	// that never says a stint finished.
	maxRaces = 1000
)

// raceLap is what the previous race lap of a stint said, kept so that the next
// one can be compared with it.
type raceLap struct {
	Number      int
	DeltaMs     int
	Reference   string
	ClassPos    int
	GapAheadMs  int
	GapBehindMs int
}

// races is what is remembered about each stint in progress, by stint id.
type races struct {
	mu   sync.Mutex
	laps map[string]raceLap
}

// remember files this lap as the stint's latest and returns the one before it,
// or nil on the stint's first lap.
func (r *races) remember(stint string, lap *plugin.LapFacts) *raceLap {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.laps == nil {
		r.laps = make(map[string]raceLap)
	}
	var prev *raceLap
	if p, ok := r.laps[stint]; ok {
		prev = &p
	} else if len(r.laps) >= maxRaces {
		// Forget everything rather than something: the cost is one quiet lap
		// per stint, once, and a map that stays a map.
		r.laps = make(map[string]raceLap)
	}
	cur := raceLap{Number: lap.Number, DeltaMs: lap.DeltaMs, Reference: lap.Reference}
	if lap.Position != nil {
		cur.ClassPos, cur.GapAheadMs, cur.GapBehindMs = lap.Position.ClassPos, lap.Position.GapAheadMs, lap.Position.GapBehindMs
	}
	r.laps[stint] = cur
	return prev
}

// forget drops a stint that finished.
func (r *races) forget(stint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.laps, stint)
}

// changes is what is different about this lap from the one before, in the
// words the model is told. Empty is silence, and silence is the usual answer.
func changes(prev *raceLap, lap *plugin.LapFacts) []string {
	if prev == nil {
		// The first lap of the stint: nothing to compare with. That includes a
		// personal best, because the server's reference is the best lap of
		// this stint and the first lap is always that.
		return nil
	}
	var out []string
	if lap.PersonalBest {
		out = append(out, "This is the driver's best lap here.")
	}
	if pos := lap.Position; pos != nil {
		if prev.ClassPos > 0 && pos.ClassPos > 0 && pos.ClassPos != prev.ClassPos {
			out = append(out, fmt.Sprintf("Position changed from P%d to P%d.", prev.ClassPos, pos.ClassPos))
		}
		if within(pos.GapBehindMs) && !within(prev.GapBehindMs) {
			out = append(out, fmt.Sprintf("The car behind has closed to %s.", seconds(pos.GapBehindMs)))
		}
		if within(pos.GapAheadMs) && !within(prev.GapAheadMs) {
			out = append(out, fmt.Sprintf("The car ahead is now %s away.", seconds(pos.GapAheadMs)))
		}
	}
	if lap.Kind == plugin.LapClean && lap.Reference != "" && lap.Reference == prev.Reference &&
		lap.DeltaMs-prev.DeltaMs >= paceDropMs {
		out = append(out, fmt.Sprintf("The lap was %s slower than the one before.", seconds(lap.DeltaMs-prev.DeltaMs)))
	}
	return out
}

// within reports a car close enough to be a fight. Zero is nobody there.
func within(gapMs int) bool { return gapMs > 0 && gapMs < closeGapMs }

// lapCompleted is a lap the server reduced to facts. Outside a race, or with
// the radio turned off, it costs nothing and remembers nothing.
func (e *Engineer) lapCompleted(ctx context.Context, cfg config, ev plugin.Event) (plugin.Usage, error) {
	if ev.Session.Type != plugin.SessionRace || !cfg.racePace {
		return plugin.Usage{}, nil
	}
	prev := e.races.remember(ev.Session.StintID, ev.Lap)
	why := changes(prev, ev.Lap)
	if len(why) == 0 {
		return plugin.Usage{}, nil
	}
	if !cfg.configured() {
		return plugin.Usage{}, plugin.ErrNotConfigured
	}

	line, use, promptVersion, err := e.racePace(ctx, cfg, ev.Lap, prev, why)
	if err != nil {
		if errors.Is(err, plugin.ErrNoAnswer) {
			// Nothing to say, or nothing usable: the driver hears nothing,
			// which is not the event failing.
			return use, nil
		}
		return use, err
	}
	if e.Store == nil {
		// Nowhere to file it, so nothing for a client to fetch: the words
		// were written and there is nobody to give them to.
		return use, nil
	}
	filed := LapCues{
		DriverSlug: ev.Driver.Slug, Job: jobRacePace, StintID: ev.Session.StintID, Lap: ev.Lap.Number,
		Session: plugin.SessionRace, Mode: CueRace, Reference: ev.Lap.Reference,
		Cues: []cueLine{{Line: line}}, Model: cfg.model, Prompt: promptVersion, WrittenAt: e.clock(),
	}
	e.speakLines(ctx, cfg, worstFirst(filed.Cues, nil))
	if err := e.Store.SaveLapCues(ctx, filed); err != nil {
		e.Log.LogAttrs(ctx, slog.LevelWarn, "the radio line was written but could not be filed",
			slog.String("stint", ev.Session.StintID), slog.Int("lap", ev.Lap.Number), slog.String("reason", err.Error()))
	}
	return use, nil
}

// racePace writes the line, and says what it cost and which prompt produced it.
func (e *Engineer) racePace(ctx context.Context, cfg config, lap *plugin.LapFacts, prev *raceLap, why []string) (string, plugin.Usage, string, error) {
	parts, promptVersion := e.prompt(ctx)
	system := inLanguage(parts[PartPersona]+"\n\n"+parts[PartRacePace], cfg.language)

	raw, spent, err := e.client(cfg).Ask(ctx, system, raceFacts(lap, prev, why), cueTool(MaxRaceWords), raceTokens)
	use := usageOf(jobRacePace, cfg.model, spent)
	if err != nil {
		return "", use, promptVersion, e.vendorError(ctx, jobRacePace, err)
	}
	var answer struct {
		Line string `json:"line"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", use, promptVersion, fmt.Errorf("%w: the line could not be read: %w", plugin.ErrNoAnswer, err)
	}
	line := strings.TrimSpace(answer.Line)
	if line == "" {
		return "", use, promptVersion, plugin.ErrNoAnswer
	}
	// The radio is about pace and position; a corner is for practice. With no
	// corners to name, a line that names a turn is discarded.
	if err := ValidateCue(cfg.language, CueRace, line, nil); err != nil {
		e.discard(ctx, CueRace, err)
		return "", use, promptVersion, err
	}
	return line, use, promptVersion, nil
}
