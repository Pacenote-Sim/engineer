//go:build postgres

package engineer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
	"github.com/pacenote-sim/protocol/wire"
)

// The store, against a real database laid out the way the host lays one out: a
// schema of this plugin's own, the server's core_read views beside it, and a
// search_path that finds this plugin's tables first.
//
// It is behind a build tag for the same reason the server's are — `go test ./...`
// has to pass on a machine with no PostgreSQL — and it is worth having because
// the one thing a plugin database is for is the join, and a join cannot be
// tested against a fake.

// EnvURL points at a PostgreSQL the tests may create schemas on.
const EnvURL = "PACENOTE_TEST_DATABASE_URL"

// plugged is a store over a schema laid out as the host would, and the DSN that
// reaches it. The migration this plugin ships is the one that is run, so a
// migration that would not apply fails here rather than on somebody's server.
func plugged(t *testing.T) (*Store, string) {
	t.Helper()
	r := require.New(t)

	dsn := os.Getenv(EnvURL)
	if dsn == "" {
		t.Skipf("%s is not set, so there is nothing to run this against", EnvURL)
	}
	ctx := context.Background()

	var b [6]byte
	_, err := rand.Read(b[:])
	r.NoError(err)
	schema := "plugin_engineer_test_" + hex.EncodeToString(b[:])

	admin, err := pgx.Connect(ctx, dsn)
	r.NoError(err)
	defer func() { _ = admin.Close(ctx) }()

	// core_read is the server's, and is created here only so the join has
	// something to join to. The columns are the ones the real view publishes.
	_, err = admin.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS core_read_`+schema+`;
		CREATE TABLE IF NOT EXISTS core_read_`+schema+`.drivers (
			id bigint, slug text PRIMARY KEY, name text NOT NULL)`)
	r.NoError(err)
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	r.NoError(err)
	t.Cleanup(func() {
		c, connErr := pgx.Connect(context.Background(), dsn)
		if connErr != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_, _ = c.Exec(context.Background(), `DROP SCHEMA IF EXISTS core_read_`+schema+` CASCADE`)
	})

	// The plugin's own migrations, exactly as shipped and in the order the host
	// applies them: every .sql in the directory, sorted by name. Naming one file
	// here would mean a migration added later is never applied in a test and is
	// first applied on somebody's server.
	files, err := filepath.Glob("migrations/*.sql")
	r.NoError(err)
	r.NotEmpty(files, "this plugin declares a database and ships no migration")
	sort.Strings(files)
	for _, f := range files {
		migration, readErr := os.ReadFile(f)
		r.NoError(readErr)
		_, err = admin.Exec(ctx, `SET search_path = `+schema+`;`+string(migration))
		r.NoErrorf(err, "%s would not apply", f)
	}

	// A DSN carrying the search_path the host sets on the role: this plugin's
	// schema first, the server's views second.
	u, err := url.Parse(dsn)
	r.NoError(err)
	q := u.Query()
	q.Set("search_path", schema+",core_read_"+schema)
	u.RawQuery = q.Encode()

	store, err := Open(ctx, u.String())
	r.NoError(err)
	t.Cleanup(store.Close)

	_, err = admin.Exec(ctx,
		`INSERT INTO core_read_`+schema+`.drivers (id, slug, name) VALUES (1, 'mihai', 'Mihai Racovita')`)
	r.NoError(err)

	return store, u.String()
}

func aDebrief() Debrief {
	return Debrief{
		StintID: "stint-1", DriverSlug: "mihai", DriverName: "Mihai", Sim: "iracing",
		Track: "Spa", Car: "GT3",
		Summary: "Eleven clean laps and a best inside your own.",
		Findings: []Finding{
			{Area: "Turn 1", Observation: "Fourteen down at the apex.", Drill: "Release the brake earlier."},
		},
		Setup: []SetupChange{{Setting: "Rear wing", From: "7", To: "6", Because: "Eleven down on the exit with the tyres even."}},
		Model: ModelSonnet, Prompt: PromptVersion,
	}
}

// filedCues is a lap's lines as this plugin files them.
func filedCues(stint string, lap int, job string, notes ...string) LapCues {
	cues := []cueLine{{Turn: 9, ApexPct: 610, Line: "Carry more speed."}, {Turn: 1, ApexPct: 44, Line: "Release earlier."}}
	if job == jobRacePace {
		cues = []cueLine{{Line: "P4, car behind at eight tenths."}}
	}
	return LapCues{
		DriverSlug: "mihai", Job: job, StintID: stint, Lap: lap, Session: plugin.SessionPractice,
		Mode: CueTraining, Reference: "your best lap", Cues: cues, SetupNotes: notes,
		Model: ModelSonnet, Prompt: PromptVersion,
	}
}

func TestADebriefIsFiledAndReadBack(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	d := aDebrief()
	d.WrittenAt = time.Now().Add(-time.Hour)
	r.NoError(store.SaveDebrief(ctx, d))

	got, err := store.Recent(ctx, "mihai", 10)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal("Eleven clean laps and a best inside your own.", got[0].Summary)
	r.Len(got[0].Findings, 1)
	r.Equal("Release the brake earlier.", got[0].Findings[0].Drill)
	r.Equal(ModelSonnet, got[0].Model)
	r.Equal("stint-1", got[0].StintID)
	r.Len(got[0].Setup, 1)
	r.Equal("Rear wing", got[0].Setup[0].Setting)

	// The same debrief, asked for by the stint the client uploaded — and only
	// by its own driver.
	byStint, err := store.DebriefByStint(ctx, "mihai", "stint-1")
	r.NoError(err)
	r.Equal(got[0].ID, byStint.ID)
	_, err = store.DebriefByStint(ctx, "someoneelse", "stint-1")
	r.ErrorIs(err, ErrNoRows, "a driver read another driver's debrief")
	_, err = store.DebriefByStint(ctx, "mihai", "stint-2")
	r.ErrorIs(err, ErrNoRows)

	// The name comes from the server's own data rather than the copy stored
	// here, which is the whole reason a plugin's tables live beside core's.
	r.Equal("Mihai Racovita", got[0].DriverName,
		"the driver's name was read from the plugin's copy rather than from core")

	// Another driver's debriefs are not this driver's.
	other, err := store.Recent(ctx, "someoneelse", 10)
	r.NoError(err)
	r.Empty(other)
}

// A driver core no longer has still has their debriefs, and they still say whose
// they were. This is why the name is stored as well as joined.
func TestADebriefOutlivesTheDriverRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, dsn := plugged(t)
	ctx := context.Background()

	r.NoError(store.SaveDebrief(ctx, aDebrief()))

	conn, err := pgx.Connect(ctx, dsn)
	r.NoError(err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `DELETE FROM drivers WHERE slug = 'mihai'`)
	r.NoError(err)

	got, err := store.Recent(ctx, "mihai", 10)
	r.NoError(err)
	r.Len(got, 1, "removing a driver from the server deleted their coaching")
	r.Equal("Mihai", got[0].DriverName, "an old debrief no longer says whose it was")
}

func TestRecentIsNewestFirstAndBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	base := time.Now().Add(-100 * time.Hour)
	for i := range 5 {
		d := aDebrief()
		d.Summary = string(rune('a' + i))
		d.WrittenAt = base.Add(time.Duration(i) * time.Hour)
		r.NoError(store.SaveDebrief(ctx, d))
	}

	got, err := store.Recent(ctx, "mihai", 3)
	r.NoError(err)
	r.Len(got, 3)
	r.Equal("e", got[0].Summary, "the newest is not first")
	r.Equal("c", got[2].Summary)

	// A limit nobody set, and one nobody should have set, both come back
	// bounded rather than reading the table.
	got, err = store.Recent(ctx, "mihai", 0)
	r.NoError(err)
	r.Len(got, 5)
	got, err = store.Recent(ctx, "mihai", 10_000)
	r.NoError(err)
	r.Len(got, 5)
}

func TestPruneRemovesWhatIsPastKeeping(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	old := aDebrief()
	old.Summary = "last season"
	old.WrittenAt = time.Now().Add(-200 * 24 * time.Hour)
	r.NoError(store.SaveDebrief(ctx, old))

	recent := aDebrief()
	recent.Summary = "this week"
	r.NoError(store.SaveDebrief(ctx, recent))

	// Zero days keeps everything, which is the operator saying so.
	removed, err := store.Prune(ctx, 0)
	r.NoError(err)
	r.Zero(removed)

	oldCues := filedCues("stint-old", 3, jobCues)
	oldCues.WrittenAt = time.Now().Add(-200 * 24 * time.Hour)
	r.NoError(store.SaveLapCues(ctx, oldCues))
	r.NoError(store.SaveLapCues(ctx, filedCues("stint-1", 7, jobCues)))

	removed, err = store.Prune(ctx, DefaultKeepDays)
	r.NoError(err)
	r.EqualValues(2, removed, "the old debrief and the old cues are two rows")

	kept, err := store.RecentLapCues(ctx, "mihai", 10)
	r.NoError(err)
	r.Len(kept, 1)
	r.Equal("stint-1", kept[0].StintID)

	got, err := store.Recent(ctx, "mihai", 10)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal("this week", got[0].Summary)
}

// A debrief written with no findings at all is a legitimate debrief — the stint
// was unremarkable — and must not come back as a null nobody can range over.
func TestADebriefWithNoFindings(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	d := aDebrief()
	d.Findings = nil
	d.Summary = "A tidy stint with nothing worth changing."
	r.NoError(store.SaveDebrief(ctx, d))

	got, err := store.Recent(ctx, "mihai", 10)
	r.NoError(err)
	r.Len(got, 1)
	r.Empty(got[0].Findings)
}

// A store that was never opened answers rather than panicking, because a plugin
// running without a database is a supported way to run it.
func TestAStoreThatIsNotThere(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	var none *Store
	r.Error(none.SaveDebrief(ctx, aDebrief()))
	_, err := none.Recent(ctx, "mihai", 10)
	r.Error(err)
	removed, err := none.Prune(ctx, 30)
	r.NoError(err, "pruning a database that is not there is not a failure")
	r.Zero(removed)
	r.NotPanics(none.Close)

	r.Error(none.SaveLapCues(ctx, filedCues("s", 1, jobCues)))
	_, err = none.LapCuesFor(ctx, "mihai", "s", 1)
	r.Error(err)
	_, err = none.RecentLapCues(ctx, "mihai", 5)
	r.Error(err)
	_, err = none.NotesFor(ctx, "mihai", "s")
	r.Error(err)
	_, err = none.DebriefByStint(ctx, "mihai", "s")
	r.Error(err)
	_, err = none.Profile(ctx, "mihai")
	r.Error(err)
	r.Error(none.SaveProfile(ctx, Profile{DriverSlug: "mihai"}))
	_, err = none.Drivers(ctx)
	r.Error(err)
	_, err = none.RecentAll(ctx, 5)
	r.Error(err)
	_, err = none.Prompts(ctx)
	r.Error(err)
	_, err = none.SavePart(ctx, PartCue, "x", "")
	r.Error(err)
	r.Error(none.ClearPart(ctx, PartCue))
}

func TestOpenRefusesWhatIsNotAConnectionString(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, err := Open(context.Background(), "this is not a connection string")
	r.Error(err)
	r.Contains(err.Error(), "not one")

	// One that parses and will not connect.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = Open(ctx, "postgres://nobody:nothing@127.0.0.1:1/pacenote?sslmode=disable&connect_timeout=1")
	r.Error(err)
	r.Contains(err.Error(), "would not answer")
}

// A finished stint filed end to end: the model answers, the debrief is written,
// and it is there to read afterwards. This is the whole plugin in one test.
func TestAFinishedStintIsWrittenUpAndFiled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)

	e, _, asked := vendorSaying(t, debriefOut{
		Summary: "Eleven clean laps and a best inside your own.",
		Findings: []Finding{{
			Area: "Turn 1", Observation: "Fourteen kilometres per hour down at the apex.",
			Drill: "Release the brake earlier and let the car run to the apex.",
		}},
	})
	e.Store = store

	use, err := e.Notify(t.Context(), stintFinished())
	r.NoError(err)
	r.EqualValues(420, use.Total())
	r.Equal("debrief", use.Job)
	r.NotEmpty(*asked)

	filed, err := store.Recent(t.Context(), "mihai", 10)
	r.NoError(err)
	r.Len(filed, 1)
	r.Contains(filed[0].Summary, "Eleven clean laps")
	r.Len(filed[0].Findings, 1)
	r.Equal("Spa", filed[0].Track)
	r.Equal("stint-1", filed[0].StintID, "the client cannot ask for this debrief by its stint")
	r.Equal(ModelSonnet, filed[0].Model)
	r.Equal(PromptVersion, filed[0].Prompt,
		"a debrief that cannot be traced to the prompt that wrote it")
}

// A debrief that was written and could not be filed is still a debrief. Failing
// the event would make the host log this plugin as broken over a row.
func TestADebriefThatCannotBeFiledIsStillADebrief(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)

	e, out, _ := vendorSaying(t, debriefOut{Summary: "A tidy stint."})
	e.Store = store
	store.Close() // the database goes away between the answer and the filing

	use, err := e.Notify(t.Context(), stintFinished())
	r.NoError(err, "a debrief that could not be filed failed the whole event")
	r.EqualValues(420, use.Total())
	r.Contains(out.String(), "could not be filed")
}

// Every part of the prompt: kept, read back exactly, and used.
func TestAWrittenPromptIsKeptAndUsed(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	// Nothing written is not an error. It is the binary's own text.
	got, err := store.Prompts(ctx)
	r.NoError(err)
	r.Empty(got)

	e := New(store, nil)
	parts, version := e.prompt(ctx)
	r.Equal(PromptVersion, version)
	for _, info := range Parts() {
		r.Equal(info.BuiltIn, parts[info.Part])
	}

	// An operator changes one part. The rest stay as shipped.
	const mine = "Write two sentences, never three, and never mention the weather.\n"
	saved, err := store.SavePart(ctx, PartCue, mine, "ana@example.com")
	r.NoError(err)
	r.Equal(mine, saved.Markdown, "it was reformatted on the way in")
	r.Len(saved.SHA256, 64)
	r.Equal("ana@example.com", saved.UpdatedBy)

	e.forget()
	parts, oneChanged := e.prompt(ctx)
	r.Equal(mine, parts[PartCue], "the written part is not the one in use")
	r.Equal(persona, parts[PartPersona], "changing one part changed another")
	r.NotEqual(PromptVersion, oneChanged, "an answer written with a changed prompt is stamped as if it were not")
	r.True(strings.HasPrefix(oneChanged, PromptVersion+"+"))

	// A second part changes the stamp again: what produced an answer is the
	// combination, not any one piece.
	_, err = store.SavePart(ctx, PartDebrief, "Two sentences and no findings.\n", "bea@example.com")
	r.NoError(err)
	e.forget()
	_, twoChanged := e.prompt(ctx)
	r.NotEqual(oneChanged, twoChanged, "changing a second part left the stamp alone")

	// Putting one back leaves the other alone.
	r.NoError(store.ClearPart(ctx, PartCue))
	e.forget()
	parts, backToOne := e.prompt(ctx)
	r.Equal(cueRules, parts[PartCue], "it did not go back to the built-in one")
	r.Equal("Two sentences and no findings.\n", parts[PartDebrief])
	r.NotEqual(oneChanged, backToOne)

	// And clearing the last one goes back to the bare version.
	r.NoError(store.ClearPart(ctx, PartDebrief))
	e.forget()
	parts, none := e.prompt(ctx)
	r.Equal(PromptVersion, none)
	for _, info := range Parts() {
		r.Equal(info.BuiltIn, parts[info.Part])
	}
}

// An empty part is refused by the column and by the method, because a part that
// is blank is not a part an operator meant to write.
func TestAnEmptyPartIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)

	_, err := store.SavePart(context.Background(), PartPersona, "   \n\t ", "ana@example.com")
	r.Error(err)
}

// The operator's page reads every driver's; a driver's own page reads one.
func TestTheOperatorsPageReadsEveryDriver(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	first := aDebrief()
	first.DriverSlug = "ana-lopez"
	r.NoError(store.SaveDebrief(ctx, first))
	second := aDebrief()
	second.DriverSlug = "bea-ortiz"
	r.NoError(store.SaveDebrief(ctx, second))

	all, err := store.RecentAll(ctx, 10)
	r.NoError(err)
	r.Len(all, 2)

	mine, err := store.Recent(ctx, "ana-lopez", 10)
	r.NoError(err)
	r.Len(mine, 1)
	r.Equal("ana-lopez", mine[0].DriverSlug)
}

// The lines said about a lap are filed by stint and lap, one row per job, and
// fetched together; a lap posted twice replaces itself; a driver reads only
// their own.
func TestLapCuesAreFiledAndFetched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	r.NoError(store.SaveLapCues(ctx, filedCues("stint-1", 7, jobCues, "The front washes out.")))
	r.NoError(store.SaveLapCues(ctx, filedCues("stint-1", 7, jobRacePace)))
	r.NoError(store.SaveLapCues(ctx, filedCues("stint-1", 8, jobCues, "Still the front.", "And the rear on power.")))

	rows, err := store.LapCuesFor(ctx, "mihai", "stint-1", 7)
	r.NoError(err)
	r.Len(rows, 2, "the corner lines and the radio line are two rows for one lap")
	got := merged(rows)
	r.Len(got.Cues, 3)
	r.Equal(1, got.Cues[0].Turn)
	r.Equal(0, got.Cues[2].Turn)
	r.Equal([]string{"The front washes out."}, got.SetupNotes)

	// No lap named is the latest lap anything was written for.
	rows, err = store.LapCuesFor(ctx, "mihai", "stint-1", 0)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(8, rows[0].Lap)

	// Another driver, or a lap nothing was written for, is nothing.
	rows, err = store.LapCuesFor(ctx, "someoneelse", "stint-1", 7)
	r.NoError(err)
	r.Empty(rows)
	rows, err = store.LapCuesFor(ctx, "mihai", "stint-1", 99)
	r.NoError(err)
	r.Empty(rows)

	// A lap posted twice replaces what it said.
	again := filedCues("stint-1", 7, jobCues)
	again.Cues = []cueLine{{Turn: 1, ApexPct: 44, Line: "Said differently."}}
	r.NoError(store.SaveLapCues(ctx, again))
	rows, err = store.LapCuesFor(ctx, "mihai", "stint-1", 7)
	r.NoError(err)
	r.Len(rows, 2)
	r.Equal("Said differently.", merged(rows).Cues[0].Line)

	// The notes so far, in lap order, flattened.
	notes, err := store.NotesFor(ctx, "mihai", "stint-1")
	r.NoError(err)
	r.Equal([]string{"Still the front.", "And the rear on power."}, notes, "the replaced lap's note was kept, or the order is wrong")

	// And the driver's own page reads newest first.
	recent, err := store.RecentLapCues(ctx, "mihai", 10)
	r.NoError(err)
	r.Len(recent, 3)
	r.Equal(plugin.SessionPractice, recent[0].Session)
	r.Equal(CueTraining, recent[0].Mode)
}

// The notes that reach the next prompt are the last few, not the whole stint.
func TestOnlyTheLastNotesAreRead(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	for lap := 1; lap <= 12; lap++ {
		r.NoError(store.SaveLapCues(ctx, filedCues("stint-1", lap, jobCues, fmt.Sprintf("note %d", lap))))
	}
	notes, err := store.NotesFor(ctx, "mihai", "stint-1")
	r.NoError(err)
	r.Len(notes, maxNotesRead)
	r.Equal("note 5", notes[0])
	r.Equal("note 12", notes[len(notes)-1])
}

// A posted lap, end to end: the client posts, the lines come back, and the
// client can ask for them again — but not for another driver's.
func TestAPostedLapIsFiledAndFetchedAgain(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)

	e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
	e.Store = store

	res := postReport(t, e, aReport())
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	r.Len(cuesOf(t, res).Cues, 1)

	fetch := func(caller plugin.Caller, query string) plugin.HTTPResponse {
		got, err := e.ServeHTTP(t.Context(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/cues", Query: query, Caller: caller})
		r.NoError(err)
		return got
	}
	again := fetch(mihai, "stint=stint-1&lap=7")
	r.Equal(http.StatusOK, again.Status, string(again.Body))
	r.Equal(cuesOf(t, res).Cues, cuesOf(t, again).Cues)
	latest := fetch(mihai, "stint=stint-1")
	r.Equal(http.StatusOK, latest.Status)
	r.Equal(7, cuesOf(t, latest).Lap)

	r.Equal(http.StatusNotFound, fetch(plugin.Caller{DriverSlug: "someoneelse"}, "stint=stint-1&lap=7").Status,
		"a driver fetched another driver's cues")
	r.Equal(http.StatusNotFound, fetch(mihai, "stint=stint-1&lap=8").Status)
}

// The radio line is filed too, from the server's own facts, so the client can
// fetch it the same way.
func TestTheRadioLineIsFiled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)

	e, _, _ := vendorSaying(t, cueAnswer{Line: "P4, car behind at eight tenths."})
	e.Store = store
	e.Connected(&fakeHost{audio: []byte("ID3radio")})
	_, err := e.Notify(t.Context(), raceEvent("race-1", 3, &plugin.Position{ClassPos: 5}, 400))
	r.NoError(err)
	use, err := e.Notify(t.Context(), raceEvent("race-1", 4, &plugin.Position{ClassPos: 4, GapBehindMs: 800}, 400))
	r.NoError(err)
	r.EqualValues(420, use.Total())

	rows, err := store.LapCuesFor(t.Context(), "mihai", "race-1", 4)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(jobRacePace, rows[0].Job)
	r.Equal(CueRace, rows[0].Mode)
	r.Equal(plugin.SessionRace, rows[0].Session)
	r.Equal([]cueLine{{Line: "P4, car behind at eight tenths.", Audio: []byte("ID3radio"), AudioType: "audio/mpeg"}}, rows[0].Cues,
		"the radio line was filed without its audio")
}

// A reading is written when the page is opened and there is something new to
// read, served from the file otherwise, and rewritten when a debrief arrives.
func TestTheDriverPageWritesTheReadingOnceAndThenServesIt(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	first := aDebrief()
	first.WrittenAt = time.Now().Add(-2 * time.Hour)
	r.NoError(store.SaveDebrief(ctx, first))
	second := aDebrief()
	second.Summary = "Tidier, and the braking is coming."
	second.WrittenAt = time.Now().Add(-time.Hour)
	r.NoError(store.SaveDebrief(ctx, second))

	e, _, asked := vendorSaying(t, profileOut{
		Summary:    "A driver who brakes too late into slow corners and knows it.",
		Weaknesses: []Weakness{{Area: "Slow corners", Evidence: "Fourteen down at the apex, twice.", Trend: "improving"}},
	})
	e.Store = store
	open := func(caller plugin.Caller) plugin.HTTPResponse {
		res, err := e.ServeHTTP(ctx, plugin.HTTPRequest{
			Method: http.MethodGet, Path: "/drivers/mihai", Caller: caller, Settings: settings(), Secrets: secrets(),
		})
		r.NoError(err)
		return res
	}

	// Nothing read yet: the overview says so.
	res, err := e.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/"})
	r.NoError(err)
	r.Contains(string(res.Body), "Mihai Racovita", "the driver's name did not come from core")
	r.Contains(string(res.Body), "2 debrief(s)")
	r.Contains(string(res.Body), "Not read yet.")

	// The first open writes it and pays for it; the debriefs reached the model
	// oldest first.
	res = open(plugin.Caller{AdminEmail: "ana@example.com"})
	r.Equal(http.StatusOK, res.Status)
	r.Contains(string(res.Body), "A driver who brakes too late")
	r.Contains(string(res.Body), "Slow corners")
	r.Contains(string(res.Body), `<span class="pill">improving</span>`)
	r.Contains(string(res.Body), "Tidier, and the braking is coming.", "the debriefs are not under the reading")
	r.Equal("profile", res.Usage.Job)
	r.EqualValues(420, res.Usage.Total())
	r.Contains(*asked, "2 debrief(s), oldest first")
	r.Less(strings.Index(*asked, "Eleven clean laps"), strings.Index(*asked, "Tidier"), "not oldest first")

	// The second open serves what was filed, for nothing.
	*asked = ""
	res = open(plugin.Caller{AdminEmail: "ana@example.com"})
	r.Contains(string(res.Body), "A driver who brakes too late")
	r.Zero(res.Usage.Total(), "a reading nothing had changed was written again")
	r.Empty(*asked)

	// The overview and the driver's own page show it; the driver's page never
	// writes it.
	res, err = e.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/"})
	r.NoError(err)
	r.Contains(string(res.Body), "A driver who brakes too late")
	r.NotContains(string(res.Body), "Not read yet")
	res, err = e.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/me", Caller: mihai})
	r.NoError(err)
	r.Contains(string(res.Body), "The coach's reading")
	r.Contains(string(res.Body), "A driver who brakes too late")
	r.Empty(*asked)

	// A new debrief makes it stale: the overview says so, and the next open
	// rewrites it.
	third := aDebrief()
	third.Summary = "Best inside your own again."
	r.NoError(store.SaveDebrief(ctx, third))
	res, err = e.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/"})
	r.NoError(err)
	r.Contains(string(res.Body), "a newer debrief since this was read")
	res = open(plugin.Caller{AdminEmail: "ana@example.com"})
	r.EqualValues(420, res.Usage.Total(), "a stale reading was served as if fresh")
	r.Contains(*asked, "3 debrief(s)")

	// Filed, with what it covers.
	p, err := store.Profile(ctx, "mihai")
	r.NoError(err)
	r.Equal(3, p.Debriefs)
	r.Equal("Mihai Racovita", p.DriverName)
	r.Len(p.Weaknesses, 1)
	r.Equal(PromptVersion, p.Prompt)
}

// Without a key the page still shows the debriefs and says what is missing;
// with a vendor that fails it says that, and shows the last reading if any.
func TestTheDriverPageWithoutAKeyOrAVendor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()
	r.NoError(store.SaveDebrief(ctx, aDebrief()))

	e, _, _ := vendorSaying(t, profileOut{Summary: "x"})
	e.Store = store
	res, err := e.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/drivers/mihai", Settings: settings()})
	r.NoError(err)
	r.Equal(http.StatusOK, res.Status)
	r.Contains(string(res.Body), "Set an Anthropic key")
	r.Contains(string(res.Body), "Eleven clean laps", "the debriefs are not shown without a key")
	r.Zero(res.Usage.Total())

	down, out, _ := vendorReplying(t, http.StatusInternalServerError, `{"error":{"message":"overloaded"}}`)
	down.Store = store
	res, err = down.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/drivers/mihai", Settings: settings(), Secrets: secrets()})
	r.NoError(err)
	r.Equal(http.StatusOK, res.Status)
	r.Contains(string(res.Body), "could not read this driver just now")
	r.Contains(out.String(), "nothing was said")

	// A driver nobody has written about.
	res, err = e.ServeHTTP(ctx, plugin.HTTPRequest{Method: http.MethodGet, Path: "/drivers/nobody", Settings: settings(), Secrets: secrets()})
	r.NoError(err)
	r.Contains(string(res.Body), "Nothing yet")
}

// A stint with a sheet: the setup changes are filed with the debrief and the
// client reads them back by the stint, as JSON.
func TestSetupChangesAreFiledWithTheDebriefAndReadBack(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)

	e, _, asked := vendorSaying(t, debriefOut{
		Summary: "The car was working against you.",
		Setup:   []SetupChange{{Setting: "Rear wing", From: "7", To: "6", Because: "Eleven down on the exit with even tyres."}},
	})
	e.Store = store
	// The laps noted something first.
	r.NoError(store.SaveLapCues(t.Context(), filedCues("stint-1", 7, jobCues, "The rear steps out on the throttle.")))

	ev := stintFinished()
	ev.Stint.Setup, _ = json.Marshal(wire.CarSetup{Values: []wire.SetupValue{{Name: "Rear wing", Text: "7"}}})
	_, err := e.Notify(t.Context(), ev)
	r.NoError(err)
	r.Contains(*asked, "The rear steps out on the throttle.", "the laps' notes did not reach the debrief")

	res, err := e.ServeHTTP(t.Context(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/debrief", Query: "stint=stint-1", Caller: mihai})
	r.NoError(err)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	var got debriefView
	r.NoError(json.Unmarshal(res.Body, &got))
	r.Equal("stint-1", got.StintID)
	r.Equal("The car was working against you.", got.Summary)
	r.Len(got.Setup, 1)
	r.Equal("6", got.Setup[0].To)
	r.NotNil(got.Findings, "an empty list came back as null")

	// Another driver, and a stint with no debrief.
	res, err = e.ServeHTTP(t.Context(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/debrief", Query: "stint=stint-1", Caller: plugin.Caller{DriverSlug: "someoneelse"}})
	r.NoError(err)
	r.Equal(http.StatusNotFound, res.Status)
	res, err = e.ServeHTTP(t.Context(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/debrief", Query: "stint=stint-9", Caller: mihai})
	r.NoError(err)
	r.Equal(http.StatusNotFound, res.Status)

	// And the driver's own page shows the lap-by-lap lines and the change.
	res, err = e.ServeHTTP(t.Context(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/me", Caller: mihai})
	r.NoError(err)
	r.Contains(string(res.Body), "Lap by lap")
	r.Contains(string(res.Body), "Turn 1: Release earlier.")
	r.Contains(string(res.Body), "About the car: The rear steps out on the throttle.")
	r.Contains(string(res.Body), "Rear wing")
	r.Contains(string(res.Body), "7 to 6")
}

// Every driver with a debrief is on the overview, with the newest first.
func TestDriversAreListedMostRecentFirst(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	store, _ := plugged(t)
	ctx := context.Background()

	ana := aDebrief()
	ana.DriverSlug, ana.DriverName = "ana-lopez", "Ana"
	ana.WrittenAt = time.Now().Add(-time.Hour)
	r.NoError(store.SaveDebrief(ctx, ana))
	r.NoError(store.SaveDebrief(ctx, aDebrief()))
	r.NoError(store.SaveDebrief(ctx, aDebrief()))
	r.NoError(store.SaveProfile(ctx, Profile{DriverSlug: "ana-lopez", Summary: "Steady.", Through: 1}))

	rows, err := store.Drivers(ctx)
	r.NoError(err)
	r.Len(rows, 2)
	r.Equal("mihai", rows[0].Slug)
	r.Equal("Mihai Racovita", rows[0].Name, "the name is not core's")
	r.Equal(2, rows[0].Debriefs)
	r.Empty(rows[0].Summary)
	r.Equal("ana-lopez", rows[1].Slug)
	r.Equal("Ana", rows[1].Name, "a driver core does not have lost their name")
	r.Equal("Steady.", rows[1].Summary)
	r.Equal(rows[1].Through < rows[1].Newest, rows[1].Stale())

	// A profile is one per driver, replaced on save.
	r.NoError(store.SaveProfile(ctx, Profile{DriverSlug: "ana-lopez", Summary: "Quicker.", Through: rows[1].Newest, Debriefs: 1}))
	p, err := store.Profile(ctx, "ana-lopez")
	r.NoError(err)
	r.Equal("Quicker.", p.Summary)
	_, err = store.Profile(ctx, "nobody")
	r.ErrorIs(err, ErrNoRows)
}
