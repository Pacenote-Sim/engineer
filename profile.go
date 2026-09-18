package engineer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pacenote-sim/plugin"
)

// The operator's reading of a driver.
//
// Six debriefs are six good pages nobody reads twice. What an operator wants
// from them is the pattern: what this driver keeps losing time to, and whether
// it is moving. That is the profile — the coach reading its own debriefs as one
// story — and it is written when an administrator opens the driver's page and
// there is something new to read. Not on a schedule and not on every stint:
// the operator chose to pay for it when they look, and only then.

const (
	// jobProfile is what a reading is metered as.
	jobProfile = "profile"
	// profileTokens bounds the answer: three sentences and three weaknesses.
	profileTokens = 900
	// profileDebriefs is how many of a driver's debriefs a reading considers.
	// A dozen is a season for a weekend driver and a fortnight for a busy one;
	// further back is a different driver.
	profileDebriefs = 12
	// MaxWeaknesses is how many a reading may name — three, for the same
	// reason a debrief has three findings.
	MaxWeaknesses = 3
)

// Weakness is one thing a driver keeps losing time to.
type Weakness struct {
	// Area is what it is about: a corner type, braking, consistency, tyres.
	Area string `json:"area"`
	// Evidence is what the debriefs said, quoted or close to it.
	Evidence string `json:"evidence"`
	// Trend is improving, steady, worse or new.
	Trend string `json:"trend"`
}

// trends are the words a trend may be. Anything else becomes "steady", which
// is the claim that needs the least evidence.
var trends = map[string]bool{"improving": true, "steady": true, "worse": true, "new": true}

// Profile is the coach's reading of one driver, as filed.
type Profile struct {
	DriverSlug string
	DriverName string
	Summary    string
	Weaknesses []Weakness
	// Through is the newest debrief this reading considered. A debrief written
	// since makes it stale, and the next open rewrites it.
	Through int64
	// Debriefs is how many it was read from.
	Debriefs  int
	Model     string
	Prompt    string
	UpdatedAt time.Time
}

// errNothingToRead is a driver with no debriefs yet.
var errNothingToRead = errors.New("engineer: there are no debriefs to read this driver from")

// readDriver is the profile for the page: the filed one when nothing has
// changed since it was written, a new one otherwise. The usage is what a new
// one cost, and zero when the filed one served.
//
// On failure it still returns whatever was filed, stale or not, so the page can
// show the last reading beside the reason there is no newer one.
func (e *Engineer) readDriver(ctx context.Context, cfg config, slug string) (Profile, plugin.Usage, error) {
	debriefs, err := e.Store.Recent(ctx, slug, profileDebriefs)
	if err != nil {
		return Profile{}, plugin.Usage{}, err
	}
	if len(debriefs) == 0 {
		return Profile{}, plugin.Usage{}, errNothingToRead
	}
	var newest int64
	for i := range debriefs {
		newest = max(newest, debriefs[i].ID)
	}

	have, err := e.Store.Profile(ctx, slug)
	switch {
	case err == nil && have.Through >= newest:
		return have, plugin.Usage{}, nil
	case err != nil && !isNoRows(err):
		return Profile{}, plugin.Usage{}, err
	case !cfg.configured():
		return have, plugin.Usage{}, plugin.ErrNotConfigured
	}

	parts, promptVersion := e.prompt(ctx)
	system := inLanguage(parts[PartPersona]+"\n\n"+parts[PartProfile], cfg.language)
	raw, spent, err := e.client(cfg).Ask(ctx, system, profileFacts(debriefs), profileTool(), profileTokens)
	use := usageOf(jobProfile, cfg.model, spent)
	if err != nil {
		return have, use, e.vendorError(ctx, jobProfile, err)
	}

	var answer struct {
		Summary    string     `json:"summary"`
		Weaknesses []Weakness `json:"weaknesses"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return have, use, fmt.Errorf("%w: the reading could not be read: %w", plugin.ErrNoAnswer, err)
	}
	answer.Summary = strings.TrimSpace(answer.Summary)
	weaknesses := cleanWeaknesses(answer.Weaknesses)
	if answer.Summary == "" && len(weaknesses) == 0 {
		return have, use, plugin.ErrNoAnswer
	}

	p := Profile{
		DriverSlug: slug, DriverName: debriefs[0].DriverName, Summary: answer.Summary, Weaknesses: weaknesses,
		Through: newest, Debriefs: len(debriefs), Model: cfg.model, Prompt: promptVersion, UpdatedAt: e.clock(),
	}
	if err := e.Store.SaveProfile(ctx, p); err != nil {
		e.Log.LogAttrs(ctx, slog.LevelWarn, "the reading was written but could not be filed",
			slog.String("driver", slug), slog.String("reason", err.Error()))
	}
	return p, use, nil
}

// cleanWeaknesses keeps what was asked for: a few, each about something, with
// a trend that is one of the four words.
func cleanWeaknesses(in []Weakness) []Weakness {
	out := make([]Weakness, 0, MaxWeaknesses)
	for _, w := range in {
		w.Area, w.Evidence = strings.TrimSpace(w.Area), strings.TrimSpace(w.Evidence)
		if w.Area == "" && w.Evidence == "" {
			continue
		}
		w.Trend = strings.ToLower(strings.TrimSpace(w.Trend))
		if !trends[w.Trend] {
			w.Trend = "steady"
		}
		out = append(out, w)
		if len(out) == MaxWeaknesses {
			break
		}
	}
	return out
}

// pageOverview is GET /: every driver the coach has written about, with the
// reading of each where there is one.
func (e *Engineer) pageOverview(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	if e.Store == nil {
		return plugin.HTML(http.StatusOK, page("Drivers",
			`  <div class="card"><p class="muted">This plugin has no database, so nothing it wrote was kept.</p></div>`)), nil
	}
	rows, err := e.Store.Drivers(ctx)
	if err != nil {
		return plugin.HTML(http.StatusBadGateway, page("Drivers",
			`  <div class="card"><p class="warn">The drivers could not be read just now.</p></div>`)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, `  <div class="card">
    <p class="muted">Every driver this plugin has written about. Open one for the coach's reading of them —
what repeats, and whether it is moving — written when the page is opened and there is something new
to read.</p>
    <p><a class="ghost" href="%s">What the coach is asked</a></p>
  </div>
`, esc(r.Prefix+"/agent"))
	if len(rows) == 0 {
		b.WriteString(`  <div class="card">
    <h2>Nothing yet</h2>
    <p class="muted">A debrief is written when a stint finishes, if the operator asked for one.</p>
  </div>
`)
		return plugin.HTML(http.StatusOK, page("Drivers", b.String())), nil
	}
	b.WriteString(`  <div class="card">
    <table class="facts rows">
`)
	for _, d := range rows {
		reading := "Not read yet."
		switch {
		case d.Summary != "" && d.Stale():
			reading = esc(d.Summary) + ` <span class="faint">— a newer debrief since this was read</span>`
		case d.Summary != "":
			reading = esc(d.Summary)
		}
		fmt.Fprintf(&b, `      <tr><th><a href="%s">%s</a></th><td>%d debrief(s) · last %s<div class="drill">%s</div></td></tr>
`,
			esc(r.Prefix+"/drivers/"+d.Slug), esc(plainOr(d.Name, d.Slug)), d.Debriefs,
			esc(d.Last.Format(time.RFC822)), reading)
	}
	b.WriteString(`    </table>
  </div>
`)
	return plugin.HTML(http.StatusOK, page("Drivers", b.String())), nil
}

// pageDriver is GET /drivers/<slug>: the reading, then the debriefs it was
// read from. Opening it is what writes the reading, so what it cost is on the
// response.
func (e *Engineer) pageDriver(ctx context.Context, r plugin.HTTPRequest, slug string) (plugin.HTTPResponse, error) {
	if !plugin.ValidName(slug) {
		return plugin.Text(http.StatusNotFound, "There is no driver at that address."), nil
	}
	if e.Store == nil {
		return plugin.HTML(http.StatusOK, page(slug,
			`  <div class="card"><p class="muted">This plugin has no database, so nothing it wrote was kept.</p></div>`)), nil
	}

	cfg := configOf(r.Settings, r.Secrets)
	p, use, err := e.readDriver(ctx, cfg, slug)
	title := plainOr(p.DriverName, slug)

	var b strings.Builder
	switch {
	case errors.Is(err, errNothingToRead):
		b.WriteString(`  <div class="card">
    <h2>Nothing yet</h2>
    <p class="muted">No debrief has been written for this driver, so there is nothing to read them from.</p>
  </div>
`)
	case errors.Is(err, plugin.ErrNotConfigured):
		b.WriteString(`  <div class="notice">Set an Anthropic key on this plugin's settings page to get the coach's reading of this driver.</div>
`)
		b.WriteString(profileCard(p, true))
	case err != nil:
		e.Log.LogAttrs(ctx, slog.LevelWarn, "a driver could not be read", slog.String("driver", slug), slog.String("reason", err.Error()))
		b.WriteString(`  <div class="notice warn">The coach could not read this driver just now.</div>
`)
		b.WriteString(profileCard(p, true))
	default:
		b.WriteString(profileCard(p, false))
	}

	list, err := e.Store.Recent(ctx, slug, 20)
	if err != nil {
		b.WriteString(`  <div class="card"><p class="warn">The debriefs could not be read just now.</p></div>
`)
	} else {
		b.WriteString(debriefList(list, false))
	}
	res := plugin.HTML(http.StatusOK, page(title, b.String()))
	res.Usage = use
	return res, nil
}

// profileCard renders a reading. stale says a newer debrief exists that it
// does not cover; an empty reading renders nothing.
func profileCard(p Profile, stale bool) string {
	if p.Summary == "" && len(p.Weaknesses) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`  <div class="card">
    <h2>The coach's reading</h2>
`)
	fmt.Fprintf(&b, `    <p class="said">%s</p>
`, esc(p.Summary))
	if len(p.Weaknesses) > 0 {
		b.WriteString(`    <table class="facts rows">
`)
		for _, w := range p.Weaknesses {
			fmt.Fprintf(&b, `      <tr><th>%s</th><td>%s <span class="pill">%s</span></td></tr>
`, esc(plainOr(w.Area, "in general")), esc(w.Evidence), esc(w.Trend))
		}
		b.WriteString(`    </table>
`)
	}
	note := ""
	if stale {
		note = " · a newer debrief since"
	}
	fmt.Fprintf(&b, `    <p class="faint">Read from %d debrief(s) · %s%s</p>
  </div>
`, p.Debriefs, esc(p.UpdatedAt.Format(time.RFC822)), note)
	return b.String()
}
