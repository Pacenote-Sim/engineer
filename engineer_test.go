package engineer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
	"github.com/pacenote-sim/protocol/wire"
)

// The plugin is tested against a vendor that answers however the test says. What
// is being tested is everything around the model: what it is asked, what is done
// with what comes back, and — most of all — what is thrown away.
//
// There is no test here that a particular prompt produces a particular sentence.
// That is not a thing that can be asserted, and pretending otherwise would be a
// suite that fails when the vendor improves.

// vendorSaying is a plugin whose vendor answers with the tool input given.
func vendorSaying(t *testing.T, answer any) (*Engineer, *logs, *string) {
	t.Helper()
	body, err := json.Marshal(answer)
	require.NoError(t, err)
	return vendorReplying(t, http.StatusOK, `{
		"content": [{"type": "tool_use", "name": "`+toolNameFor(answer)+`", "input": `+string(body)+`}],
		"usage": {"input_tokens": 400, "output_tokens": 20}
	}`)
}

// toolNameFor picks the tool a fake answer belongs to, so a test writes the
// answer and not the envelope.
func toolNameFor(answer any) string {
	switch answer.(type) {
	case lapAnswer:
		return "cues"
	case cueAnswer:
		return "cue"
	case profileOut:
		return "profile"
	default:
		return "debrief"
	}
}

// lapAnswer is what the vendor says about a posted lap.
type lapAnswer struct {
	Cues       []turnLine `json:"cues"`
	SetupNotes []string   `json:"setup_notes,omitempty"`
}

type turnLine struct {
	Turn int    `json:"turn"`
	Line string `json:"line"`
}

// cueAnswer is one line, for the radio.
type cueAnswer struct {
	Line string `json:"line"`
}

// profileOut is a reading of a driver.
type profileOut struct {
	Summary    string     `json:"summary"`
	Weaknesses []Weakness `json:"weaknesses"`
}

// vendorReplying is a plugin whose vendor answers with exactly this.
func vendorReplying(t *testing.T, status int, body string) (*Engineer, *logs, *string) {
	t.Helper()
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		asked = string(raw)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	out := &logs{}
	e := New(nil, slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.BaseURL = srv.URL
	e.now = func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) }
	return e, out, &asked
}

type logs struct{ buf bytes.Buffer }

func (l *logs) Write(p []byte) (int, error) { return l.buf.Write(p) }
func (l *logs) String() string              { return l.buf.String() }

// settings and secrets are what the host lends a configured plugin.
func settings() plugin.Values {
	return plugin.Values{
		SettingModel: ModelSonnet, SettingDebriefs: "true", SettingRacePace: "true",
		SettingSetup: "true", SettingSpeak: "true", SettingKeepDays: "180",
	}
}

func secrets() plugin.Secrets {
	return plugin.Secrets{SettingAPIKey: plugin.NewSecret("sk-ant-notarealkey")}
}

// mihai is the driver the host says is calling.
var mihai = plugin.Caller{DriverSlug: "mihai", DriverName: "Mihai"}

// aReport is a plausible lap as a client posts it: two corners that lost time,
// the worse one first, on a circuit whose length is known.
func aReport() lapReport {
	return lapReport{
		StintID: "stint-1", Lap: 7, LapMs: 138400, SpokenLap: "one minute 38.4 seconds",
		Session: plugin.SessionPractice, TrackLengthM: spa, Reference: "your best lap", DeltaMs: 900,
		Corners: []corner{
			{
				Turn: 1, ApexPct: 44, ApexKmh: 44, RefApexKmh: 58, DeficitKmh: 14,
				BrakeAtApex: 12, Pattern: wire.PatternLateBraking,
			},
			{Turn: 9, ApexPct: 610, ApexKmh: 110, RefApexKmh: 116, DeficitKmh: 6},
		},
	}
}

// postReport posts a report the way the client does: signed in and configured,
// unless a test changes the request.
func postReport(t *testing.T, e *Engineer, rep lapReport, change ...func(*plugin.HTTPRequest)) plugin.HTTPResponse {
	t.Helper()
	body, err := json.Marshal(rep)
	require.NoError(t, err)
	r := plugin.HTTPRequest{
		Method: http.MethodPost, Path: "/laps", Body: body,
		Caller: mihai, Settings: settings(), Secrets: secrets(),
	}
	for _, c := range change {
		c(&r)
	}
	res, err := e.ServeHTTP(t.Context(), r)
	require.NoError(t, err)
	return res
}

// cuesOf reads the answer to a posted lap.
func cuesOf(t *testing.T, res plugin.HTTPResponse) LapCues {
	t.Helper()
	require.Equal(t, "application/json; charset=utf-8", res.Header.Get("Content-Type"))
	var out LapCues
	require.NoError(t, json.Unmarshal(res.Body, &out), string(res.Body))
	return out
}

// aLap is a lap as the server reduces it to facts, for the radio.
func aLap() *plugin.LapFacts {
	corners, _ := json.Marshal([]corner{
		{Turn: 1, ApexPct: 44, ApexKmh: 44, RefApexKmh: 58, DeficitKmh: 14},
	})
	return &plugin.LapFacts{
		Number: 7, LapMs: 138400, Kind: plugin.LapClean,
		DeltaMs: 900, Reference: "your best lap", SpokenLap: "one minute 38.4 seconds",
		Corners: corners,
	}
}

func aStint() *plugin.StintFacts {
	return &plugin.StintFacts{
		Laps: 14, CleanLaps: 11, BestLapMs: 138400, AvgLapMs: 139800,
		ConsistencyPct: 82, TopSpeedKmh: 241,
		Fuel:  plugin.FuelSummary{UsedL: 44.2, RemainingL: 12.1},
		Tyres: plugin.TyreSummary{LF: 92, RF: 88, LR: 84, RR: 83},
	}
}

// stintFinished is the event the host delivers at the end of a session.
func stintFinished() plugin.Event {
	return plugin.Event{
		Kind:     plugin.EventStintFinished,
		Driver:   plugin.Driver{Slug: "mihai", Name: "Mihai"},
		Session:  plugin.Session{StintID: "stint-1", Sim: "iracing", Track: "Spa", Car: "GT3"},
		Stint:    aStint(),
		Settings: settings(),
		Secrets:  secrets(),
	}
}

func TestAPluginWithNoKeyIsNotConfigured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Brake later."}}})
	res := postReport(t, e, aReport(), func(r *plugin.HTTPRequest) { r.Secrets = nil })
	r.Equal(http.StatusServiceUnavailable, res.Status,
		"a plugin with no key must say so, not fail at the vendor")
	r.Empty(*asked)
}

func TestAStrangerCannotPostALap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, lapAnswer{})
	res := postReport(t, e, aReport(), func(r *plugin.HTTPRequest) { r.Caller = plugin.Caller{} })
	r.Equal(http.StatusUnauthorized, res.Status)
	r.Empty(*asked)
}

func TestAGoodLapGetsItsLines(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, lapAnswer{Cues: []turnLine{
		// Given worst first; heard in track order.
		{Turn: 9, Line: "Carry more speed through the exit."},
		{Turn: 1, Line: "Still braking at the apex of Turn 1. Release earlier."},
	}})
	res := postReport(t, e, aReport())
	r.Equal(http.StatusOK, res.Status, string(res.Body))

	out := cuesOf(t, res)
	r.Equal("stint-1", out.StintID)
	r.Equal(7, out.Lap)
	r.Equal(CueTraining, out.Mode)
	r.Equal("your best lap", out.Reference)
	r.Len(out.Cues, 2)
	r.Equal(1, out.Cues[0].Turn, "the lines are not in the order they are heard")
	r.Equal(44, out.Cues[0].ApexPct, "the client is not told where to speak it")
	r.Equal("Still braking at the apex of Turn 1. Release earlier.", out.Cues[0].Line)
	r.Equal(9, out.Cues[1].Turn)
	r.Equal(610, out.Cues[1].ApexPct)
	r.Equal(PromptVersion, out.Prompt)
	r.Equal(ModelSonnet, out.Model)

	// What it cost is on the response, named by job and model, because "call"
	// is not an answer to where the money went.
	r.Equal("cues", res.Usage.Job)
	r.Equal(ModelSonnet, res.Usage.Model)
	r.EqualValues(400, res.Usage.InputTokens)
	r.EqualValues(20, res.Usage.OutputTokens)

	// The facts reached the model, already exact. The model narrates; it never
	// computes.
	r.Contains(*asked, "Lap 7, one minute 38.4 seconds.")
	r.Contains(*asked, "Against your best lap: 0.90 seconds slower.")
	r.Contains(*asked, "The circuit is 7004 metres round.")
	r.Contains(*asked, "Turn 1: apex 44 kilometres per hour against 58 on the reference, 14 down")
	r.Contains(*asked, "braking too late")
	r.Contains(*asked, "These are the only turns you may name")
	r.Contains(*asked, "This is practice")
	r.NotContains(*asked, "can be changed", "the setup was closed and the car was mentioned")
}

// The rule that matters most, end to end: a line for a corner that was not in
// the report, a line that names another corner, and a second line for a corner
// are all dropped — and the lines that pass are still returned.
func TestALineForTheWrongTurnIsDropped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, out, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{
		{Turn: 5, Line: "Brake later into Turn 5."},
		{Turn: 1, Line: "Brake later into Turn 9."},
		{Turn: 9, Line: "Carry more speed."},
		{Turn: 9, Line: "And again."},
	}})
	res := postReport(t, e, aReport())
	r.Equal(http.StatusOK, res.Status)

	got := cuesOf(t, res)
	r.Len(got.Cues, 1, "a dropped line took the good ones with it, or was not dropped")
	r.Equal(9, got.Cues[0].Turn)
	r.Equal("Turn 9. Carry more speed.", got.Cues[0].Line, "a line that named no corner has its own put first")

	// It still cost money, and the operator is told.
	r.EqualValues(400, res.Usage.InputTokens)
	r.Contains(out.String(), "a cue was discarded")
	r.Contains(out.String(), "turn 5, which was not in the report")
	r.Contains(out.String(), "names turn 9, which is not in this line is for turn 1")
	r.Contains(out.String(), "turn 9 already has a line")
}

// A model that writes the turn in quotes has still answered.
func TestATurnInQuotesIsStillATurn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"cues","input":{"cues":[{"turn":"9","line":"Carry more speed."},{"turn":1,"line":"Release earlier."}]}}],"usage":{"input_tokens":400,"output_tokens":20}}`)
	res := postReport(t, e, aReport())
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	got := cuesOf(t, res)
	r.Len(got.Cues, 2)
	r.Equal(1, got.Cues[0].Turn)
	r.Equal(9, got.Cues[1].Turn)

	// A whole number written as a float is the same number; one note written
	// bare is one note; a number that is not a turn is still refused, and the
	// answer is in the log for whoever improves the prompt.
	e3, _, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"cues","input":{"cues":[{"turn":8.0,"line":"Later."}],"setup_notes":"The front washes out."}}],"usage":{}}`)
	open := aReport()
	open.SetupOpen = true
	open.Corners = append(open.Corners, corner{Turn: 8, ApexPct: 520})
	res = postReport(t, e3, open)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	r.Equal(8, cuesOf(t, res).Cues[0].Turn)
	r.Equal([]string{"The front washes out."}, cuesOf(t, res).SetupNotes)

	// The whole answer once more as a string inside "cues", which is exactly
	// what the model sent on the first real lap this plugin coached.
	e4, _, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"cues","input":{"cues":"{\"cues\":[{\"turn\":9,\"line\":\"Pick up the throttle earlier.\"},{\"turn\":1,\"line\":\"Hold the brake later.\"}],\"setup_notes\":[\"The front washes out.\"]}"}}],"usage":{"input_tokens":2137,"output_tokens":117}}`)
	res = postReport(t, e4, open)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	r.Len(cuesOf(t, res).Cues, 2)
	r.Equal("Turn 1. Hold the brake later.", cuesOf(t, res).Cues[0].Line)
	r.Equal([]string{"The front washes out."}, cuesOf(t, res).SetupNotes)

	// And the list alone as a string.
	e5, _, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"cues","input":{"cues":"[{\"turn\":1,\"line\":\"Hold the brake later.\"}]"}}],"usage":{}}`)
	res = postReport(t, e5, aReport())
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	r.Len(cuesOf(t, res).Cues, 1)

	e2, out, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"cues","input":{"cues":[{"turn":"nine","line":"x"}]}}],"usage":{}}`)
	res = postReport(t, e2, aReport())
	r.Equal(http.StatusBadGateway, res.Status)
	r.Contains(string(res.Body), "could not be read")
	r.Contains(out.String(), "the cues could not be read")
	r.Contains(out.String(), `\"nine\"`, "the unreadable answer is not in the log")
}

// A race gets the shorter limit, and the prompt says which it is.
func TestARaceLapGetsTheShorterLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	long := strings.TrimSpace(strings.Repeat("word ", 13))
	e, _, asked := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: long}}})

	race := aReport()
	race.Session = plugin.SessionRace
	res := postReport(t, e, race)
	r.Equal(http.StatusOK, res.Status)
	r.Empty(cuesOf(t, res).Cues, "thirteen words reached a driver who is racing")
	r.Equal(CueRace, cuesOf(t, res).Mode)
	r.Contains(*asked, "This is a race")

	// The same line is fine in practice, where there is nobody to race.
	res = postReport(t, e, aReport())
	r.Len(cuesOf(t, res).Cues, 1)
}

// A model deciding there is nothing worth saying is the answer the prompt asks
// for when the lap was unremarkable, and it is not a failure.
func TestNothingWorthSayingIsAnEmptyList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "   "}}})
	res := postReport(t, e, aReport())
	r.Equal(http.StatusOK, res.Status)
	r.Empty(cuesOf(t, res).Cues)
	r.Contains(string(res.Body), `"cues":[]`, "an empty list came back as null")
	r.EqualValues(400, res.Usage.InputTokens, "a call that said nothing was reported as free")
}

// A lap that lost time nowhere has nothing to coach and costs nothing.
func TestALapWithNoCornersCostsNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Brake later."}}})
	rep := aReport()
	rep.Corners = nil
	res := postReport(t, e, rep)
	r.Equal(http.StatusOK, res.Status)
	r.Empty(cuesOf(t, res).Cues)
	r.Zero(res.Usage.Total())
	r.Empty(*asked, "the vendor was called about a lap with nothing in it")
}

func TestABadReportIsRefused(t *testing.T) {
	t.Parallel()

	fortyOne := aReport()
	fortyOne.Corners = nil
	for i := 1; i <= MaxCornersPerLap+1; i++ {
		fortyOne.Corners = append(fortyOne.Corners, corner{Turn: i})
	}
	twice := aReport()
	twice.Corners = append(twice.Corners, corner{Turn: 1})
	noStint := aReport()
	noStint.StintID = ""
	noLap := aReport()
	noLap.Lap = 0
	warmup := aReport()
	warmup.Session = "warmup"
	outside := aReport()
	outside.Corners[0].ApexPct = 1001

	for _, tc := range []struct {
		name string
		rep  lapReport
		says string
	}{
		{"no stint", noStint, "stint_id"},
		{"no lap number", noLap, "lap must be"},
		{"a session that is not one", warmup, "session must be"},
		{"too many corners", fortyOne, "limit is 40"},
		{"a turn listed twice", twice, "turn 1 is listed twice"},
		{"an apex outside the lap", outside, "outside the lap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e, _, asked := vendorSaying(t, lapAnswer{})
			res := postReport(t, e, tc.rep)
			r.Equal(http.StatusBadRequest, res.Status)
			r.Contains(string(res.Body), tc.says)
			r.Empty(*asked, "a report that was refused still reached the vendor")
		})
	}

	t.Run("not JSON at all", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e, _, _ := vendorSaying(t, lapAnswer{})
		res, err := e.ServeHTTP(t.Context(), plugin.HTTPRequest{
			Method: http.MethodPost, Path: "/laps", Body: []byte(`{not json`),
			Caller: mihai, Settings: settings(), Secrets: secrets(),
		})
		r.NoError(err)
		r.Equal(http.StatusBadRequest, res.Status)
		r.Contains(string(res.Body), "not readable JSON")
	})
}

// Notes about the car are asked for and kept only while the setup is open. On
// a fixed setup nothing about the car is asked, and anything volunteered is
// dropped, because a driver who cannot change the car does not need to hear
// what to change.
func TestSetupNotesOnlyWhenTheSetupIsOpen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, lapAnswer{
		Cues:       []turnLine{{Turn: 1, Line: "Brake later."}},
		SetupNotes: []string{"  The front  washes out mid-corner. ", "", "The rear steps out on the throttle.", "A third."},
	})

	closed := postReport(t, e, aReport())
	r.Empty(cuesOf(t, closed).SetupNotes, "notes about the car were kept on a fixed setup")
	r.NotContains(*asked, "can be changed")
	r.NotContains(*asked, "setup_notes", "the schema asked about the car on a fixed setup")

	open := aReport()
	open.SetupOpen = true
	res := postReport(t, e, open)
	got := cuesOf(t, res).SetupNotes
	r.Equal([]string{"The front washes out mid-corner.", "The rear steps out on the throttle."}, got,
		"notes were not trimmed, or an empty one was kept, or more than two were")
	r.Contains(*asked, "The car can be changed in this session.")
	r.Contains(*asked, "Nothing has been noted about the car this stint.",
		"without a database there are no earlier notes, and the prompt should say so")
	r.Contains(*asked, "setup_notes")
}

func TestWhenTheVendorRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		body   string
		logged string
		says   string
	}{
		{
			name: "a key that is not a key", status: http.StatusUnauthorized,
			body:   `{"error":{"message":"invalid x-api-key"}}`,
			logged: "will keep happening until it is fixed",
			says:   "the vendor refused",
		},
		{
			name: "going too fast", status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"rate limit reached"}}`,
			logged: "the vendor was busy",
			says:   "the vendor was busy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			e, out, _ := vendorReplying(t, tc.status, tc.body)
			res := postReport(t, e, aReport())
			r.Equal(http.StatusBadGateway, res.Status,
				"at the moment a driver is waiting, every failure is the client speaking its own line")
			// The client's author reads why in the body, in a few words and
			// never the vendor's own text; the operator reads the rest in the log.
			r.Contains(string(res.Body), tc.says)
			r.NotContains(string(res.Body), "invalid x-api-key")
			r.Contains(out.String(), tc.logged)
		})
	}
}

// An answer the model returned in a shape nothing can read is discarded like any
// other unusable answer, and still paid for.
func TestAnAnswerThatWillNotParse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"cues","input":{"cues":"a string"}}],"usage":{"input_tokens":400,"output_tokens":20}}`)
	res := postReport(t, e, aReport())
	r.Equal(http.StatusBadGateway, res.Status)
	r.EqualValues(420, res.Usage.Total(), "an unreadable answer was reported as free")
}

// A deadline that has gone is the one unforgivable failure, and it is not worth
// a log line every lap: the host already knows, and a driver waiting for a cue
// that will not come does not need it written down twice.
func TestAMissedDeadlineIsQuiet(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = io.WriteString(w, `{"content":[],"usage":{}}`)
	}))
	defer srv.Close()
	defer close(release)

	out := &logs{}
	e := New(nil, slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.BaseURL = srv.URL

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	body, _ := json.Marshal(aReport())
	res, err := e.ServeHTTP(ctx, plugin.HTTPRequest{
		Method: http.MethodPost, Path: "/laps", Body: body, Caller: mihai, Settings: settings(), Secrets: secrets(),
	})
	r.NoError(err)
	r.Equal(http.StatusBadGateway, res.Status)
	r.Contains(string(res.Body), "the deadline passed")
	r.NotContains(out.String(), "level=ERROR")
	r.NotContains(out.String(), "level=WARN",
		"a missed deadline was logged, and it happens once a lap")
}

// Anything that is not the vendor refusing still reaches the operator, because
// a coach that has quietly stopped working is the failure nobody notices.
func TestAnUnexpectedFailureIsLogged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, out, _ := vendorReplying(t, http.StatusOK, `{not json`)
	res := postReport(t, e, aReport())
	r.Equal(http.StatusBadGateway, res.Status)
	r.Contains(out.String(), "the coach could not answer")
}

func TestADebriefIsRenderedForReading(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, debriefOut{
		Summary: "Eleven clean laps and a best inside your own. The car was working.",
		Findings: []Finding{
			{
				Area: "Turn 1", Observation: "You are 14 kilometres per hour down at the apex.",
				Drill: "Release the brake earlier and let the car run to the apex.",
			},
			{
				Area: "Consistency", Observation: "82 out of 100, which is a second of spread.",
				Drill: "Run five laps trying to repeat the same lap rather than a faster one.",
			},
		},
	})
	use, err := e.Notify(t.Context(), stintFinished())
	r.NoError(err)
	r.Equal("debrief", use.Job)
	r.EqualValues(420, use.Total())

	// The stint's own numbers reached the model.
	r.Contains(*asked, "14 laps, 11 of them clean")
	r.Contains(*asked, "Consistency 82 out of 100")
	r.Contains(*asked, "3.16 litres a lap")
	r.NotContains(*asked, "setup_changes", "no sheet arrived and the car was asked about")

	// And the prose a driver reads is built the same way every time.
	text := debriefOut{
		Summary:  "A tidy stint.",
		Findings: []Finding{{Area: "Turn 1", Observation: "Fourteen down.", Drill: "Release earlier."}},
		Setup:    []SetupChange{{Setting: "Rear wing", From: "7", To: "6", Because: "The exit speed."}},
	}.Text()
	r.Contains(text, "Turn 1. Fourteen down. Release earlier.")
	r.Contains(text, "Setup — Rear wing: 7 to 6. The exit speed.")
}

// The server never sends a clean-lap count, so zero is "not told" and the
// debrief must not turn it into "none of them clean".
func TestAStintWithNoCleanLapCountSaysNothingAboutCleanLaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := aStint()
	s.CleanLaps = 0
	got := stintFacts(s)
	r.Contains(got, "14 laps.\n")
	r.NotContains(got, "clean")
}

// Setup changes are asked for only when a sheet arrived and the operator wants
// them, and anything volunteered otherwise is dropped.
func TestSetupChangesComeOnlyWithASheet(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, debriefOut{
		Summary: "A tidy stint.",
		Setup: []SetupChange{
			{Setting: " Rear wing ", From: "7", To: "6", Because: "The exit speed is 11 down with the tyres even."},
			{Setting: "", To: "x", Because: "nothing"},
			{Setting: "Brake bias", To: "", Because: "nothing"},
			{Setting: "LF camber", To: "-3.5", Because: "Inner edge 12 degrees hotter."},
			{Setting: "RF camber", To: "-3.5", Because: "Outer edge 11 degrees hotter."},
			{Setting: "LR camber", To: "-2.0", Because: "A fourth."},
		},
	})
	cfg := configOf(settings(), secrets())

	// No sheet: not asked for, not kept.
	out, _, _, err := e.debrief(t.Context(), cfg, aStint(), nil, false)
	r.NoError(err)
	r.Empty(out.Setup, "changes to a car that has no sheet were kept")
	r.NotContains(*asked, "setup_changes")

	// A sheet: asked for, kept within what was asked for, and the notes from
	// the laps are in front of the model.
	out, _, _, err = e.debrief(t.Context(), cfg, aStint(), []string{"The front washes out mid-corner."}, true)
	r.NoError(err)
	r.Len(out.Setup, 3, "an empty change was kept, or more than three were")
	r.Equal("Rear wing", out.Setup[0].Setting, "a change was not trimmed")
	r.Equal("LF camber", out.Setup[1].Setting)
	r.Contains(*asked, "setup_changes")
	r.Contains(*asked, "Noted about the car earlier this stint")
	r.Contains(*asked, "The front washes out mid-corner.")

	// Through the event: a stint with a sheet and the setting on asks; the
	// setting off does not, whatever arrived.
	ev := stintFinished()
	ev.Stint.Setup, _ = json.Marshal(wire.CarSetup{Values: []wire.SetupValue{{Name: "Rear wing", Text: "7"}}})
	_, err = e.Notify(t.Context(), ev)
	r.NoError(err)
	r.Contains(*asked, "setup_changes")
	r.Contains(*asked, "Rear wing: 7")

	ev.Settings[SettingSetup] = "false"
	_, err = e.Notify(t.Context(), ev)
	r.NoError(err)
	r.NotContains(*asked, "setup_changes")
}

func TestNotifyOnlyActsOnAFinishedStint(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, debriefOut{Summary: "A tidy stint."})

	// A practice lap completing costs nothing: the cues for it come through
	// the client's own report, where somebody is holding a deadline.
	use, err := e.Notify(t.Context(), plugin.Event{
		Kind: plugin.EventLapCompleted, Lap: aLap(),
		Session:  plugin.Session{StintID: "stint-1", Type: plugin.SessionPractice},
		Settings: settings(), Secrets: secrets(),
	})
	r.NoError(err)
	r.Zero(use.Total(), "a completed lap spent money on its own")
	r.Empty(*asked, "the vendor was called for a lap nobody asked about")

	// A finished stint, with debriefs on, does.
	use, err = e.Notify(t.Context(), stintFinished())
	r.NoError(err)
	r.EqualValues(420, use.Total())
	r.NotEmpty(*asked)

	// An event this version does not carry is handled by doing nothing.
	use, err = e.Notify(t.Context(), plugin.Event{Kind: "something.else", Settings: settings(), Secrets: secrets()})
	r.NoError(err)
	r.Zero(use.Total())
}

// A debrief is the most expensive thing this plugin does and the only one
// nobody is waiting on, so it is the first thing an operator turns off.
func TestDebriefsTurnedOffCostNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, debriefOut{Summary: "A tidy stint."})
	ev := stintFinished()
	ev.Settings[SettingDebriefs] = "false"
	use, err := e.Notify(t.Context(), ev)
	r.NoError(err)
	r.Zero(use.Total())
	r.Empty(*asked, "a debrief was written with debriefs turned off")
}

func TestNotifyWithNoKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, debriefOut{Summary: "x"})
	ev := stintFinished()
	ev.Secrets = nil
	_, err := e.Notify(t.Context(), ev)
	r.ErrorIs(err, plugin.ErrNotConfigured)
}

func TestNotifyWithNoStint(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, debriefOut{Summary: "x"})
	ev := stintFinished()
	ev.Stint = nil
	use, err := e.Notify(t.Context(), ev)
	r.NoError(err)
	r.Zero(use.Total())
	r.Empty(*asked)

	// And a lap event with no lap.
	use, err = e.Notify(t.Context(), plugin.Event{Kind: plugin.EventLapCompleted, Settings: settings(), Secrets: secrets()})
	r.NoError(err)
	r.Zero(use.Total())
}

func TestADebriefThatCameBackEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, debriefOut{})
	use, err := e.Notify(t.Context(), stintFinished())
	r.ErrorIs(err, plugin.ErrNoAnswer)
	r.EqualValues(420, use.Total(), "an empty debrief was reported as free")

	// And a debrief that will not parse.
	e2, _, _ := vendorReplying(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"debrief","input":{"summary":42}}],"usage":{}}`)
	_, err = e2.Notify(t.Context(), stintFinished())
	r.ErrorIs(err, plugin.ErrNoAnswer)
}

// The settings this plugin declares have to be renderable by the panel and have
// to work with nothing configured, which is how every fresh installation starts.
func TestTheDeclaredSettingsAreValid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e := New(nil, nil)
	declared, err := e.Settings(context.Background())
	r.NoError(err)
	r.NoError(plugin.ValidateSettings(declared))

	// With nothing filled in, the defaults are what the plugin runs on — and
	// the key is required, so an empty set is a plugin that says it needs
	// configuring rather than one that calls the vendor with no key.
	_, err = plugin.ValidateValues(declared, plugin.Values{})
	r.Error(err, "the key must be required")

	values, err := plugin.ValidateValues(declared, plugin.Values{SettingAPIKey: "x"})
	r.NoError(err)
	cfg := configOf(values, secrets())
	r.Equal(ModelSonnet, cfg.model, "the middle model is the one to try first")
	r.True(cfg.debriefs)
	r.True(cfg.racePace, "the radio is off by default")
	r.True(cfg.setup, "setup advice is off by default")
	r.True(cfg.speak, "the lines are not spoken by default")
	r.Equal("en", cfg.language)
	r.Equal(DefaultKeepDays, cfg.keepDays)
	r.True(cfg.configured())
}

// A host that sends nothing at all still produces a usable configuration rather
// than a model id of the empty string.
func TestConfigFallsBackWhenTheHostSendsNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := configOf(nil, nil)
	r.Equal(ModelSonnet, cfg.model)
	r.Equal(DefaultKeepDays, cfg.keepDays)
	r.False(cfg.configured())
}

func TestTheClockDefaultsToTheRealOne(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e := New(nil, nil)
	e.now = nil
	r.WithinDuration(time.Now(), e.clock(), time.Minute,
		"a plugin built without a clock has none")
}

// Corners too close for a line each are one line, filed under the first of
// them whichever the model named; and a lap never gets more lines than the
// plan allows, however many the model wrote.
func TestJoinedCornersAreOneLineAndALapIsCapped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep := aReport()
	rep.TrackLengthM = 4000
	rep.Corners = []corner{
		{Turn: 4, ApexPct: 530, ApexKmh: 70, RefApexKmh: 80, DeficitKmh: 10, BrakeAtPct: 515},
		{Turn: 3, ApexPct: 500, ApexKmh: 80, RefApexKmh: 86, DeficitKmh: 6},
		{Turn: 1, ApexPct: 100, ApexKmh: 70, RefApexKmh: 74, DeficitKmh: 4, ExitKmh: 110},
	}
	e, out, asked := vendorSaying(t, lapAnswer{Cues: []turnLine{
		{Turn: 4, Line: "Brake once for both and carry the speed."},
		{Turn: 3, Line: "Turn 3, later."},
		{Turn: 1, Line: "Turn 1, brake later."},
	}})
	res := postReport(t, e, rep)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	got := cuesOf(t, res)
	r.Len(got.Cues, 2)
	r.Equal(1, got.Cues[0].Turn)
	r.Equal(3, got.Cues[1].Turn, "the joined line is filed under the first corner")
	r.Equal(500, got.Cues[1].ApexPct)
	r.Equal("Turn 3. Brake once for both and carry the speed.", got.Cues[1].Line)
	r.Contains(out.String(), "turn 3 already has a line", "the second line for the same pair is dropped")
	r.Contains(*asked, "Turns 3 and 4 are too close together")
	r.Contains(*asked, "at most 2 lines in all")

	// Six corners a straight apart, six lines written: four are kept.
	wide := aReport()
	wide.TrackLengthM = 6000
	wide.Corners = nil
	var lines []turnLine
	for i := 1; i <= 6; i++ {
		wide.Corners = append(wide.Corners, corner{Turn: i, ApexPct: i * 150, ApexKmh: 80, RefApexKmh: 90, DeficitKmh: 10})
		lines = append(lines, turnLine{Turn: i, Line: fmt.Sprintf("Turn %d, brake later.", i)})
	}
	e2, out2, _ := vendorSaying(t, lapAnswer{Cues: lines})
	res = postReport(t, e2, wide)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	r.Len(cuesOf(t, res).Cues, MaxCueLines)
	r.Contains(out2.String(), "the lap already has 4 lines")
}
