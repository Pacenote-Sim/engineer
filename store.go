package engineer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pacenote-sim/plugin"
)

// This plugin's own tables, in this plugin's own schema.
//
// Everything here is written unqualified. The role the host hands over carries a
// search_path that puts this plugin's schema first and the server's core_read
// views second, so "debriefs" is this plugin's table and "drivers" is the
// server's view — which is what makes the join in [Store.Recent] one query
// against one connection.
//
// Nothing in core is written from here, and nothing could be: the role is
// granted SELECT on the views and nothing at all on the tables behind them.

// Finding is one thing the debrief noticed.
type Finding struct {
	// Area is what it is about: a corner, consistency, tyres, fuel.
	Area string `json:"area"`
	// Observation is what the data showed, and Drill the one thing to do about
	// it. They are separate because a driver reads the first and practises the
	// second, and a debrief that mixes them is one nobody acts on.
	Observation string `json:"observation"`
	Drill       string `json:"drill"`
}

// MaxSetupChanges is how many changes to the car one debrief may suggest.
const MaxSetupChanges = 3

// SetupChange is one change to the car: the control as the simulator names it,
// from what it was to what to try, and the measurement behind it.
type SetupChange struct {
	Setting string `json:"setting"`
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Because string `json:"because"`
}

// fromTo says the change the way a sheet does.
func (s SetupChange) fromTo() string {
	if s.From == "" {
		return "to " + s.To
	}
	return s.From + " to " + s.To
}

// Debrief is what was written after one session.
type Debrief struct {
	ID int64
	// StintID is the server's own identifier for the stint, so the client
	// that uploaded it can ask for the debrief by it.
	StintID string
	// DriverSlug is the key. It is the server's own identifier for the driver
	// and it is what the core_read views join on.
	DriverSlug string
	DriverName string
	Sim        string
	Track      string
	Car        string
	Summary    string
	Findings   []Finding
	// Setup is the changes to the car, when the sheet was there to change.
	Setup []SetupChange
	// Model and Prompt are what produced it. They are stored because the only
	// way to tell whether a change to either made the coach better is to be
	// able to read what each one wrote.
	Model  string
	Prompt string

	WrittenAt time.Time
}

// debriefView is a debrief as a program reads it.
type debriefView struct {
	StintID   string        `json:"stint_id"`
	Track     string        `json:"track,omitempty"`
	Car       string        `json:"car,omitempty"`
	Summary   string        `json:"summary"`
	Findings  []Finding     `json:"findings"`
	Setup     []SetupChange `json:"setup_changes"`
	Model     string        `json:"model,omitempty"`
	Prompt    string        `json:"prompt_version,omitempty"`
	WrittenAt time.Time     `json:"written_at"`
}

func (d Debrief) view() debriefView {
	v := debriefView{
		StintID: d.StintID, Track: d.Track, Car: d.Car, Summary: d.Summary,
		Findings: d.Findings, Setup: d.Setup, Model: d.Model, Prompt: d.Prompt, WrittenAt: d.WrittenAt,
	}
	if v.Findings == nil {
		v.Findings = []Finding{}
	}
	if v.Setup == nil {
		v.Setup = []SetupChange{}
	}
	return v
}

// Store is this plugin's database.
type Store struct{ pool *pgxpool.Pool }

// Open connects to the database the host handed over. A plugin that declared no
// database gets [plugin.ErrNoDatabase] from [plugin.DatabaseURL] and should
// treat it as fatal at startup rather than carrying on without one.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("engineer: the connection string is not one: %w", err)
	}
	// Four is enough. This plugin writes a row a lap and reads a handful when
	// somebody opens a page; the pool exists so those do not queue behind each
	// other, not to carry load. Every plugin with a database takes its share
	// of the server's max_connections, and a plugin that hoards them is a
	// plugin that breaks the server it lives in.
	cfg.MaxConns = 4

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("engineer: the database would not open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("engineer: the database would not answer: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close returns every connection.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// errNoStore is a store that was never opened.
var errNoStore = errors.New("engineer: there is no database")

func (s *Store) ready() error {
	if s == nil || s.pool == nil {
		return errNoStore
	}
	return nil
}

// ErrNoRows is a lookup that found nothing, for callers that care.
var ErrNoRows = pgx.ErrNoRows

// isNoRows reports a lookup that found nothing.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// SaveDebrief files one. The findings and the setup changes go in as jsonb:
// nothing queries inside them, they are written once and read back whole, and
// a table of findings would buy an index nobody reads.
func (s *Store) SaveDebrief(ctx context.Context, d Debrief) error {
	if err := s.ready(); err != nil {
		return fmt.Errorf("%w to file a debrief in", err)
	}
	findings, err := json.Marshal(orEmpty(d.Findings))
	if err != nil {
		return fmt.Errorf("engineer: the findings could not be encoded: %w", err)
	}
	setup, err := json.Marshal(orEmpty(d.Setup))
	if err != nil {
		return fmt.Errorf("engineer: the setup changes could not be encoded: %w", err)
	}
	if d.WrittenAt.IsZero() {
		d.WrittenAt = time.Now()
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO debriefs
			(stint_id, driver_slug, driver_name, sim, track, car, summary, findings, setup, model, prompt_version, written_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		d.StintID, d.DriverSlug, d.DriverName, d.Sim, d.Track, d.Car, d.Summary, findings, setup,
		d.Model, d.Prompt, d.WrittenAt)
	if err != nil {
		return fmt.Errorf("engineer: the debrief could not be filed: %w", err)
	}
	return nil
}

// orEmpty encodes a nil list as an empty one, so a column holds "[]" and never
// "null".
func orEmpty[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

// debriefColumns is what every debrief query selects, in the order
// scanDebriefs reads it. One list rather than three copies, because three
// copies of a column list are three places to forget a column.
const debriefColumns = `d.id, d.stint_id, d.driver_slug, coalesce(c.name, d.driver_name),
	       d.sim, d.track, d.car, d.summary, d.findings, d.setup,
	       d.model, d.prompt_version, d.written_at`

// Recent is a driver's last debriefs, newest first.
//
// It joins to the server's own data through the core_read views, which is the
// whole reason a plugin has a database in the same place as everything else: the
// driver's name comes from core and is right even if it changed since.
func (s *Store) Recent(ctx context.Context, driverSlug string, limit int) ([]Debrief, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read debriefs from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+debriefColumns+`
		FROM debriefs d
		LEFT JOIN drivers c ON c.slug = d.driver_slug
		WHERE d.driver_slug = $1
		ORDER BY d.written_at DESC
		LIMIT $2`, driverSlug, bounded(limit))
	if err != nil {
		return nil, fmt.Errorf("engineer: the debriefs could not be read: %w", err)
	}
	defer rows.Close()
	return scanDebriefs(rows)
}

// RecentAll is [Store.Recent] across every driver, for the operator's page.
//
// It is a second query rather than a slug of "" in the first, because a filter
// that means "everyone" when it is empty is a filter that returns everyone the
// first time a caller forgets to set it.
func (s *Store) RecentAll(ctx context.Context, limit int) ([]Debrief, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read debriefs from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+debriefColumns+`
		FROM debriefs d
		LEFT JOIN drivers c ON c.slug = d.driver_slug
		ORDER BY d.written_at DESC
		LIMIT $1`, bounded(limit))
	if err != nil {
		return nil, fmt.Errorf("engineer: the debriefs could not be read: %w", err)
	}
	defer rows.Close()
	return scanDebriefs(rows)
}

// DebriefByStint is one driver's debrief for one stint, or [ErrNoRows]. The
// driver is part of the key so that a driver reads only their own.
func (s *Store) DebriefByStint(ctx context.Context, driverSlug, stintID string) (Debrief, error) {
	if err := s.ready(); err != nil {
		return Debrief{}, fmt.Errorf("%w to read a debrief from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+debriefColumns+`
		FROM debriefs d
		LEFT JOIN drivers c ON c.slug = d.driver_slug
		WHERE d.driver_slug = $1 AND d.stint_id = $2
		ORDER BY d.written_at DESC
		LIMIT 1`, driverSlug, stintID)
	if err != nil {
		return Debrief{}, fmt.Errorf("engineer: the debrief could not be read: %w", err)
	}
	defer rows.Close()
	list, err := scanDebriefs(rows)
	if err != nil {
		return Debrief{}, err
	}
	if len(list) == 0 {
		return Debrief{}, ErrNoRows
	}
	return list[0], nil
}

// bounded keeps a page a page: a limit nobody set, or one nobody should have
// set, reads a screenful rather than the table.
func bounded(limit int) int {
	if limit <= 0 || limit > 100 {
		return 20
	}
	return limit
}

// scanDebriefs reads what the queries above select, in the order they select it.
func scanDebriefs(rows pgx.Rows) ([]Debrief, error) {
	var out []Debrief
	for rows.Next() {
		var d Debrief
		var findings, setup []byte
		if err := rows.Scan(&d.ID, &d.StintID, &d.DriverSlug, &d.DriverName, &d.Sim, &d.Track, &d.Car,
			&d.Summary, &findings, &setup, &d.Model, &d.Prompt, &d.WrittenAt); err != nil {
			return nil, fmt.Errorf("engineer: a debrief could not be read: %w", err)
		}
		if len(findings) > 0 {
			_ = json.Unmarshal(findings, &d.Findings)
		}
		if len(setup) > 0 {
			_ = json.Unmarshal(setup, &d.Setup)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engineer: the debriefs could not be read: %w", err)
	}
	return out, nil
}

// Prune removes what is older than the operator asked to keep — debriefs and
// the lap-by-lap cues alike. Zero days keeps everything, which is the operator
// saying so rather than a default.
func (s *Store) Prune(ctx context.Context, keepDays int) (int64, error) {
	if s == nil || s.pool == nil || keepDays <= 0 {
		return 0, nil
	}
	var removed int64
	for _, table := range []string{"debriefs", "lap_cues"} {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM `+table+` WHERE written_at < now() - make_interval(days => $1)`, keepDays)
		if err != nil {
			return removed, fmt.Errorf("engineer: old %s could not be removed: %w", table, err)
		}
		removed += tag.RowsAffected()
	}
	return removed, nil
}

// SaveLapCues files what was said about one lap. A lap posted twice — a client
// that retried — replaces what it said the first time rather than adding to it.
func (s *Store) SaveLapCues(ctx context.Context, c LapCues) error {
	if err := s.ready(); err != nil {
		return fmt.Errorf("%w to file cues in", err)
	}
	cues, err := json.Marshal(orEmpty(c.Cues))
	if err != nil {
		return fmt.Errorf("engineer: the cues could not be encoded: %w", err)
	}
	notes, err := json.Marshal(orEmpty(c.SetupNotes))
	if err != nil {
		return fmt.Errorf("engineer: the notes could not be encoded: %w", err)
	}
	if c.WrittenAt.IsZero() {
		c.WrittenAt = time.Now()
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO lap_cues
			(driver_slug, stint_id, lap, job, session, mode, reference, cues, setup_notes, model, prompt_version, written_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (stint_id, lap, job) DO UPDATE
		   SET driver_slug = EXCLUDED.driver_slug,
		       session = EXCLUDED.session,
		       mode = EXCLUDED.mode,
		       reference = EXCLUDED.reference,
		       cues = EXCLUDED.cues,
		       setup_notes = EXCLUDED.setup_notes,
		       model = EXCLUDED.model,
		       prompt_version = EXCLUDED.prompt_version,
		       written_at = EXCLUDED.written_at`,
		c.DriverSlug, c.StintID, c.Lap, c.Job, string(c.Session), string(c.Mode), c.Reference,
		cues, notes, c.Model, c.Prompt, c.WrittenAt)
	if err != nil {
		return fmt.Errorf("engineer: the cues could not be filed: %w", err)
	}
	return nil
}

// lapCueColumns is what every lap_cues query selects, in the order
// scanLapCues reads it.
const lapCueColumns = `id, driver_slug, stint_id, lap, job, session, mode, reference, cues, setup_notes,
	       model, prompt_version, written_at`

// LapCuesFor is everything written about one lap of one driver's stint — the
// corner lines and the radio line, one row each. A lap of zero is the latest
// lap anything was written for.
func (s *Store) LapCuesFor(ctx context.Context, driverSlug, stintID string, lap int) ([]LapCues, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read cues from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+lapCueColumns+`
		FROM lap_cues
		WHERE driver_slug = $1 AND stint_id = $2
		  AND lap = CASE WHEN $3::int > 0 THEN $3::int
		                 ELSE (SELECT max(lap) FROM lap_cues WHERE driver_slug = $1 AND stint_id = $2) END
		ORDER BY job`, driverSlug, stintID, lap)
	if err != nil {
		return nil, fmt.Errorf("engineer: the cues could not be read: %w", err)
	}
	defer rows.Close()
	return scanLapCues(rows)
}

// RecentLapCues is a driver's last laps with something said about them, newest
// first, for their own page.
func (s *Store) RecentLapCues(ctx context.Context, driverSlug string, limit int) ([]LapCues, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read cues from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+lapCueColumns+`
		FROM lap_cues
		WHERE driver_slug = $1
		ORDER BY written_at DESC, lap DESC, job
		LIMIT $2`, driverSlug, bounded(limit))
	if err != nil {
		return nil, fmt.Errorf("engineer: the cues could not be read: %w", err)
	}
	defer rows.Close()
	return scanLapCues(rows)
}

// maxNotesRead bounds what earlier laps' notes reach the next prompt: the last
// few are the conversation, the rest is history.
const maxNotesRead = 8

// NotesFor is what the laps of a stint noted about the car so far, oldest
// first, the last few.
func (s *Store) NotesFor(ctx context.Context, driverSlug, stintID string) ([]string, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read notes from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT setup_notes FROM lap_cues
		WHERE driver_slug = $1 AND stint_id = $2 AND job = $3
		ORDER BY lap`, driverSlug, stintID, jobCues)
	if err != nil {
		return nil, fmt.Errorf("engineer: the notes could not be read: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("engineer: a note could not be read: %w", err)
		}
		var notes []string
		_ = json.Unmarshal(raw, &notes)
		out = append(out, notes...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engineer: the notes could not be read: %w", err)
	}
	if len(out) > maxNotesRead {
		out = out[len(out)-maxNotesRead:]
	}
	return out, nil
}

func scanLapCues(rows pgx.Rows) ([]LapCues, error) {
	var out []LapCues
	for rows.Next() {
		var c LapCues
		var session, mode string
		var cues, notes []byte
		if err := rows.Scan(&c.ID, &c.DriverSlug, &c.StintID, &c.Lap, &c.Job, &session, &mode, &c.Reference,
			&cues, &notes, &c.Model, &c.Prompt, &c.WrittenAt); err != nil {
			return nil, fmt.Errorf("engineer: a lap's cues could not be read: %w", err)
		}
		c.Session, c.Mode = plugin.SessionType(session), CueKind(mode)
		c.Cues = []cueLine{}
		if len(cues) > 0 {
			_ = json.Unmarshal(cues, &c.Cues)
		}
		if len(notes) > 0 {
			_ = json.Unmarshal(notes, &c.SetupNotes)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engineer: the cues could not be read: %w", err)
	}
	return out, nil
}

// Profile is the coach's reading of a driver, or [ErrNoRows].
func (s *Store) Profile(ctx context.Context, driverSlug string) (Profile, error) {
	if err := s.ready(); err != nil {
		return Profile{}, fmt.Errorf("%w to read a profile from", err)
	}
	var p Profile
	var weaknesses []byte
	err := s.pool.QueryRow(ctx, `
		SELECT driver_slug, driver_name, summary, weaknesses, through, debriefs, model, prompt_version, updated_at
		FROM profiles WHERE driver_slug = $1`, driverSlug).
		Scan(&p.DriverSlug, &p.DriverName, &p.Summary, &weaknesses, &p.Through, &p.Debriefs, &p.Model, &p.Prompt, &p.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return Profile{}, ErrNoRows
		}
		return Profile{}, fmt.Errorf("engineer: the profile could not be read: %w", err)
	}
	if len(weaknesses) > 0 {
		_ = json.Unmarshal(weaknesses, &p.Weaknesses)
	}
	return p, nil
}

// SaveProfile files a reading, replacing the one before: there is one reading
// of a driver at a time, and the newest is the one that read the most.
func (s *Store) SaveProfile(ctx context.Context, p Profile) error {
	if err := s.ready(); err != nil {
		return fmt.Errorf("%w to file a profile in", err)
	}
	weaknesses, err := json.Marshal(orEmpty(p.Weaknesses))
	if err != nil {
		return fmt.Errorf("engineer: the weaknesses could not be encoded: %w", err)
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now()
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO profiles (driver_slug, driver_name, summary, weaknesses, through, debriefs, model, prompt_version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (driver_slug) DO UPDATE
		   SET driver_name = EXCLUDED.driver_name,
		       summary = EXCLUDED.summary,
		       weaknesses = EXCLUDED.weaknesses,
		       through = EXCLUDED.through,
		       debriefs = EXCLUDED.debriefs,
		       model = EXCLUDED.model,
		       prompt_version = EXCLUDED.prompt_version,
		       updated_at = EXCLUDED.updated_at`,
		p.DriverSlug, p.DriverName, p.Summary, weaknesses, p.Through, p.Debriefs, p.Model, p.Prompt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("engineer: the profile could not be filed: %w", err)
	}
	return nil
}

// DriverRow is one driver on the operator's overview: what was written about
// them, and the reading of them if there is one.
type DriverRow struct {
	Slug     string
	Name     string
	Debriefs int
	// Last is when the newest debrief was written and Newest its id.
	Last   time.Time
	Newest int64
	// Summary and Through are the reading's, empty and zero when there is none.
	Summary string
	Through int64
}

// Stale reports a reading that does not cover the newest debrief.
func (d DriverRow) Stale() bool { return d.Through < d.Newest }

// Drivers is every driver with a debrief, most recently written about first.
func (s *Store) Drivers(ctx context.Context) ([]DriverRow, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read drivers from", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.driver_slug, coalesce(c.name, max(d.driver_name)), count(*), max(d.written_at), max(d.id),
		       coalesce(p.summary, ''), coalesce(p.through, 0)
		FROM debriefs d
		LEFT JOIN drivers c ON c.slug = d.driver_slug
		LEFT JOIN profiles p ON p.driver_slug = d.driver_slug
		GROUP BY d.driver_slug, c.name, p.summary, p.through
		ORDER BY max(d.written_at) DESC
		LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("engineer: the drivers could not be read: %w", err)
	}
	defer rows.Close()
	var out []DriverRow
	for rows.Next() {
		var d DriverRow
		if err := rows.Scan(&d.Slug, &d.Name, &d.Debriefs, &d.Last, &d.Newest, &d.Summary, &d.Through); err != nil {
			return nil, fmt.Errorf("engineer: a driver could not be read: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engineer: the drivers could not be read: %w", err)
	}
	return out, nil
}

// Written is one part of the prompt an operator has changed.
type Written struct {
	Markdown  string
	SHA256    string
	UpdatedAt time.Time
	UpdatedBy string
}

// Prompts reads every part an operator has written. A part nobody has touched
// is absent, not empty: the binary's own text is the default and a row saying
// "the same as the default" would be a row to keep in step with it.
func (s *Store) Prompts(ctx context.Context) (map[Part]Written, error) {
	if err := s.ready(); err != nil {
		return nil, fmt.Errorf("%w to read the prompt from", err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT part, markdown, sha256, updated_at, updated_by FROM prompt_parts`)
	if err != nil {
		return nil, fmt.Errorf("engineer: the prompt would not read: %w", err)
	}
	defer rows.Close()

	out := map[Part]Written{}
	for rows.Next() {
		var part string
		var w Written
		if err := rows.Scan(&part, &w.Markdown, &w.SHA256, &w.UpdatedAt, &w.UpdatedBy); err != nil {
			return nil, fmt.Errorf("engineer: a part of the prompt would not read: %w", err)
		}
		out[Part(part)] = w
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engineer: the prompt would not read: %w", err)
	}
	return out, nil
}

// SavePart replaces one piece. The digest is computed here rather than taken
// from the caller, because it is what identifies the text on every answer.
func (s *Store) SavePart(ctx context.Context, part Part, markdown, by string) (Written, error) {
	if err := s.ready(); err != nil {
		return Written{}, fmt.Errorf("%w to keep the prompt in", err)
	}
	if strings.TrimSpace(markdown) == "" {
		return Written{}, errors.New("engineer: an empty part is not one")
	}
	sum := sha256.Sum256([]byte(markdown))
	w := Written{Markdown: markdown, SHA256: hex.EncodeToString(sum[:]), UpdatedBy: by}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO prompt_parts (part, markdown, sha256, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (part) DO UPDATE
		   SET markdown = EXCLUDED.markdown,
		       sha256 = EXCLUDED.sha256,
		       updated_at = now(),
		       updated_by = EXCLUDED.updated_by
		RETURNING updated_at`, string(part), w.Markdown, w.SHA256, w.UpdatedBy).Scan(&w.UpdatedAt)
	if err != nil {
		return Written{}, fmt.Errorf("engineer: that part would not save: %w", err)
	}
	return w, nil
}

// ClearPart puts one piece back to what the binary ships.
func (s *Store) ClearPart(ctx context.Context, part Part) error {
	if err := s.ready(); err != nil {
		return fmt.Errorf("%w to clear the prompt from", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM prompt_parts WHERE part = $1`, string(part)); err != nil {
		return fmt.Errorf("engineer: that part would not clear: %w", err)
	}
	return nil
}
