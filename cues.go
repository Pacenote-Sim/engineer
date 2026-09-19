package engineer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/pacenote-sim/plugin"

	"github.com/pacenote-sim/engineer/internal/anthropic"
)

// The corner cues: a client posts what it measured on the lap just finished
// and gets back one line per corner, to speak just before that corner on the
// next lap.
//
// One model call per lap rather than one per corner. The words in the driver's
// ear are the same, the coach sees the whole lap before it speaks, there is no
// deadline in the middle of a corner, and fifteen calls become one. The client
// holds the lines and speaks each as its corner approaches; this plugin never
// knows where the car is.

const (
	// jobCues is what a lap's corner lines are metered as.
	jobCues = "cues"
	// lapTokens bounds one lap's answer: a dozen short lines and two notes.
	lapTokens = 1600
)

// postLap is POST /laps: a report in, the lines out. The cost is on the
// response whatever came of the call, because the operator paid it either way.
func (e *Engineer) postLap(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	if !r.Caller.SignedIn() {
		return plugin.Text(http.StatusUnauthorized, "Only a signed-in driver's client can post a lap."), nil
	}
	cfg := configOf(r.Settings, r.Secrets)
	if !cfg.configured() {
		return plugin.Text(http.StatusServiceUnavailable,
			"The operator has not set a key, so there is no coach to ask. Speak your own cues."), nil
	}
	rep, err := parseLapReport(r.Body)
	if err != nil {
		return plugin.Text(http.StatusBadRequest, "That lap could not be read: "+err.Error()), nil
	}
	if len(rep.Corners) == 0 {
		// Nothing lost time, or nothing was measured. Either way there is
		// nothing to coach and nothing to pay for.
		return jsonResponse(e.nothingSaid(rep), plugin.Usage{}), nil
	}

	// What earlier laps of this stint noted about the car, so the observation
	// builds rather than restarts. Best effort: a note that cannot be read is
	// a coach with a shorter memory, not a lap with no cues.
	var notes []string
	if rep.SetupOpen && e.Store != nil {
		if notes, err = e.Store.NotesFor(ctx, r.Caller.DriverSlug, rep.StintID); err != nil {
			e.Log.LogAttrs(ctx, slog.LevelWarn, "earlier notes about the car could not be read",
				slog.String("stint", rep.StintID), slog.String("reason", err.Error()))
		}
	}

	out, use, err := e.writeCues(ctx, cfg, rep, notes)
	if err != nil {
		// A vendor that refused, a deadline that went, an answer that would
		// not parse: the client speaks its own line. The status says which
		// plainly — the client's author reads it — and the cost travels with it.
		res := plugin.Text(http.StatusBadGateway, "The coach could not answer this lap: "+because(err)+".")
		res.Usage = use
		return res, nil
	}
	out.DriverSlug = r.Caller.DriverSlug
	if e.Store != nil {
		if err := e.Store.SaveLapCues(ctx, out); err != nil {
			e.Log.LogAttrs(ctx, slog.LevelWarn, "the cues were written but could not be filed",
				slog.String("stint", rep.StintID), slog.Int("lap", rep.Lap), slog.String("reason", err.Error()))
		}
	}
	return jsonResponse(out, use), nil
}

// nothingSaid is the answer to a lap with nothing to coach.
func (e *Engineer) nothingSaid(rep lapReport) LapCues {
	return LapCues{
		Job: jobCues, StintID: rep.StintID, Lap: rep.Lap, Session: rep.Session,
		Mode: kindFor(rep.Session), Reference: rep.Reference, Cues: []cueLine{}, WrittenAt: e.clock(),
	}
}

// writeCues asks for the lines and keeps the ones that pass.
//
// Everything the prompt asked for is checked again here, line by line. A line
// that fails is dropped and the rest are kept: one bad line is not a reason to
// leave the driver with nothing for the other corners. A line for a corner
// that was not in the report, or a second line for the same corner, is dropped
// the same way, because the client would speak it in front of the wrong turn.
func (e *Engineer) writeCues(ctx context.Context, cfg config, rep lapReport, notes []string) (LapCues, plugin.Usage, error) {
	kind := kindFor(rep.Session)
	parts, promptVersion := e.prompt(ctx)
	system := parts[PartPersona] + "\n\n" + parts[PartCue] + "\n\n" + sessionLine(parts, kind)
	if rep.SetupOpen {
		system += "\n\n" + parts[PartSetup]
	}
	system = inLanguage(system, cfg.language)

	groups := planLines(rep, kind)
	raw, spent, err := e.client(cfg).Ask(ctx, system, reportFactsFor(rep, groups, notes),
		lapTool(wordsFor(kind), leadTurns(groups), rep.SetupOpen), lapTokens)
	use := usageOf(jobCues, cfg.model, spent)
	if err != nil {
		return LapCues{}, use, e.vendorError(ctx, jobCues, err)
	}

	var answer modelAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		// The one failure worth the answer itself in the log: the only way to
		// make the schema or the prompt better is to read what came back.
		e.Log.LogAttrs(ctx, slog.LevelWarn, "the cues could not be read",
			slog.String("reason", err.Error()), slog.String("answer", excerpt(raw)))
		return LapCues{}, use, fmt.Errorf("%w: the cues could not be read: %w", plugin.ErrNoAnswer, err)
	}

	out := LapCues{
		Job: jobCues, StintID: rep.StintID, Lap: rep.Lap, Session: rep.Session, Mode: kind,
		Reference: rep.Reference, Cues: []cueLine{}, Model: cfg.model, Prompt: promptVersion,
		WrittenAt: e.clock(),
	}
	// A line is filed under the group it is for — under the first corner of
	// joined corners, whichever of them the model named — and each group gets
	// one line, the lap at most MaxCueLines.
	groupOf := make(map[int]*cueGroup, len(rep.Corners))
	for i := range groups {
		for _, t := range groups[i].Turns() {
			groupOf[t] = &groups[i]
		}
	}
	said := make(map[int]bool, len(answer.Cues))
	for _, a := range answer.Cues {
		line := strings.TrimSpace(a.Line)
		if line == "" {
			continue
		}
		turn := int(a.Turn)
		g, ok := groupOf[turn]
		switch {
		case !ok:
			e.discard(ctx, kind, &CueError{Rule: fmt.Sprintf("it is for turn %d, which was not in the report", turn), Line: line})
			continue
		case said[g.Lead().Turn]:
			e.discard(ctx, kind, &CueError{Rule: fmt.Sprintf("turn %d already has a line", g.Lead().Turn), Line: line})
			continue
		case len(out.Cues) >= MaxCueLines:
			e.discard(ctx, kind, &CueError{Rule: fmt.Sprintf("the lap already has %d lines", MaxCueLines), Line: line})
			continue
		}
		line = EnsureTurnNamed(cfg.language, line, g.Lead().Turn)
		if err := ValidateGroupLine(cfg.language, kind, line, *g); err != nil {
			e.discard(ctx, kind, err)
			continue
		}
		said[g.Lead().Turn] = true
		out.Cues = append(out.Cues, cueLine{Turn: g.Lead().Turn, ApexPct: g.Lead().ApexPct, Line: line})
	}
	sortCues(out.Cues)
	if rep.SetupOpen {
		out.SetupNotes = cleanNotes([]string(answer.SetupNotes))
	}
	if !rep.Early && !rep.Silent {
		e.speakLines(ctx, cfg, worstFirst(out.Cues, ranks(rep.Corners)))
	}
	return out, use, nil
}

// discard is the one log line a dropped cue gets. It is informational: the
// client speaks its own line, and the only person who wants to read it is the
// one improving the prompt.
func (e *Engineer) discard(ctx context.Context, kind CueKind, err error) {
	e.Log.LogAttrs(ctx, slog.LevelInfo, "a cue was discarded",
		slog.String("kind", string(kind)), slog.String("reason", err.Error()))
}

// kindFor is which word limit a session gets: the shorter one when there is
// somebody to race.
func kindFor(s plugin.SessionType) CueKind {
	if s == plugin.SessionRace {
		return CueRace
	}
	return CueTraining
}

func wordsFor(kind CueKind) int {
	if kind == CueRace {
		return MaxRaceWords
	}
	return MaxTrainingWords
}

// sessionLine is the one sentence of the prompt that differs between a race
// and everything else.
func sessionLine(parts map[Part]string, kind CueKind) string {
	if kind == CueRace {
		return parts[PartRace]
	}
	return parts[PartPractice]
}

// cleanNotes keeps what the model noted about the car within what was asked
// for: a few, short, and not blank.
func cleanNotes(in []string) []string {
	var out []string
	for _, n := range in {
		n = strings.Join(strings.Fields(n), " ")
		if n == "" {
			continue
		}
		if r := []rune(n); len(r) > maxNoteRunes {
			n = string(r[:maxNoteRunes])
		}
		out = append(out, n)
		if len(out) == MaxSetupNotes {
			break
		}
	}
	return out
}

// getCues is GET /cues?stint=<id>&lap=<n>: what was written about a lap, for
// the client that wants it again — after a reconnect, or for the radio line
// written from the server's own facts, which no client posted. Without a lap
// it is the latest lap of the stint. A driver reads only their own.
func (e *Engineer) getCues(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	if !r.Caller.SignedIn() {
		return plugin.Text(http.StatusUnauthorized, "Sign in to read your cues."), nil
	}
	q, _ := url.ParseQuery(r.Query)
	stint := strings.TrimSpace(q.Get("stint"))
	if stint == "" || !identifier(stint) || len(stint) > 64 {
		return plugin.Text(http.StatusBadRequest, "Say which stint: ?stint=<id>, and ?lap=<n> for one lap."), nil
	}
	lap := 0
	if v := q.Get("lap"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return plugin.Text(http.StatusBadRequest, "lap must be a lap number, from 1."), nil
		}
		lap = n
	}
	if e.Store == nil {
		return plugin.Text(http.StatusServiceUnavailable, "This plugin has no database, so nothing it wrote was kept."), nil
	}
	rows, err := e.Store.LapCuesFor(ctx, r.Caller.DriverSlug, stint, lap)
	if err != nil {
		return plugin.Text(http.StatusBadGateway, "The cues could not be read just now."), nil
	}
	if len(rows) == 0 {
		return plugin.Text(http.StatusNotFound, "Nothing has been written for that lap."), nil
	}
	return jsonResponse(merged(rows), plugin.Usage{}), nil
}

// getDebrief is GET /debrief?stint=<id>: the debrief and the setup changes for
// a stint, for the client app the driver opens when the stint is over. A
// driver reads only their own.
func (e *Engineer) getDebrief(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	if !r.Caller.SignedIn() {
		return plugin.Text(http.StatusUnauthorized, "Sign in to read your debrief."), nil
	}
	q, _ := url.ParseQuery(r.Query)
	stint := strings.TrimSpace(q.Get("stint"))
	if stint == "" || !identifier(stint) || len(stint) > 64 {
		return plugin.Text(http.StatusBadRequest, "Say which stint: ?stint=<id>."), nil
	}
	if e.Store == nil {
		return plugin.Text(http.StatusServiceUnavailable, "This plugin has no database, so nothing it wrote was kept."), nil
	}
	d, err := e.Store.DebriefByStint(ctx, r.Caller.DriverSlug, stint)
	switch {
	case err == nil:
		return jsonResponse(d.view(), plugin.Usage{}), nil
	case isNoRows(err):
		return plugin.Text(http.StatusNotFound, "No debrief has been written for that stint."), nil
	default:
		return plugin.Text(http.StatusBadGateway, "The debrief could not be read just now."), nil
	}
}

// jsonResponse is an answer for a program rather than a person.
func jsonResponse(v any, use plugin.Usage) plugin.HTTPResponse {
	b, err := json.Marshal(v)
	if err != nil {
		return plugin.Text(http.StatusInternalServerError, "The answer could not be encoded.")
	}
	return plugin.HTTPResponse{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		Body:   b,
		Usage:  use,
	}
}

// turnNumber is a turn as a model writes it: a number, or the same number in
// quotes. The schema asks for an integer; a model that answers "8" has still
// answered, and a line thrown away over the quotes is a line the driver does
// not hear.
type turnNumber int

func (t *turnNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != float64(int(f)) || f < 0 {
		return fmt.Errorf("%q is not a turn number", s)
	}
	*t = turnNumber(int(f))
	return nil
}

// noteList is the notes about the car as a model writes them: a list, or one
// note on its own when there is only one. Anything else is no notes rather
// than no answer — the lines are the point, the notes are a side remark.
type noteList []string

func (n *noteList) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*n = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*n = noteList{one}
		return nil
	}
	*n = nil
	return nil
}

// excerpt is the start of an answer, for a log line: enough to see its shape,
// not enough to fill the log.
func excerpt(raw []byte) string {
	const most = 400
	if len(raw) <= most {
		return string(raw)
	}
	return string(raw[:most]) + "…"
}

// because says, in a few words a client's author can act on, why the coach
// did not answer. It names the class of failure and never the vendor's text,
// which belongs in the plugin's own log.
func because(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "the deadline passed before the vendor answered"
	case errors.Is(err, anthropic.ErrRefused) && anthropic.Retryable(err):
		return "the vendor was busy; try the next lap"
	case errors.Is(err, anthropic.ErrRefused):
		return "the vendor refused the operator's key or request, and the plugin's log says why"
	case errors.Is(err, anthropic.ErrNoToolUse):
		return "the vendor answered without the structured reply the plugin asked for"
	default:
		return "the answer could not be read"
	}
}

// answerLine is one line of a lap's answer.
type answerLine struct {
	Turn turnNumber `json:"turn"`
	Line string     `json:"line"`
}

// modelAnswer is what the model returns for a lap. It is read the way a model
// writes it rather than only the way the schema asked: the lines as a list, or
// the whole answer once more as a string inside "cues" — a shape models
// produce with nested schemas often enough that refusing it would cost a lap
// of coaching a week.
type modelAnswer struct {
	Cues       []answerLine
	SetupNotes noteList
}

func (a *modelAnswer) UnmarshalJSON(b []byte) error {
	var raw struct {
		Cues       json.RawMessage `json:"cues"`
		SetupNotes noteList        `json:"setup_notes"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	a.SetupNotes = raw.SetupNotes
	if len(raw.Cues) == 0 || string(raw.Cues) == "null" {
		return nil
	}
	if raw.Cues[0] == '"' {
		// The answer, encoded once more as a string. Inside it is either the
		// list, or the whole object again.
		var inner string
		if err := json.Unmarshal(raw.Cues, &inner); err != nil {
			return err
		}
		var list []answerLine
		if err := json.Unmarshal([]byte(inner), &list); err == nil {
			a.Cues = list
			return nil
		}
		var whole modelAnswer
		if err := json.Unmarshal([]byte(inner), &whole); err != nil {
			return fmt.Errorf("cues is a string that is not a list of lines: %w", err)
		}
		a.Cues = whole.Cues
		if len(a.SetupNotes) == 0 {
			a.SetupNotes = whole.SetupNotes
		}
		return nil
	}
	return json.Unmarshal(raw.Cues, &a.Cues)
}

// inLanguage adds the language the coach writes in to a system prompt, and the
// words a driver in that language is spoken to with.
func inLanguage(system, code string) string {
	if extra := languageLine(code); extra != "" {
		system += "\n\n" + extra
	}
	return system + "\n\n" + speaksLine(code)
}
