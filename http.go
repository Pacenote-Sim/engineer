package engineer

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pacenote-sim/plugin"
)

// The pages this plugin serves, and what it accepts.
//
// There are two readers and they are not the same person. A driver reads their
// own coaching and nothing else — not another driver's, and not the persona. An
// operator reads everyone's and owns what the coach sounds like. The host
// decides which of the two is asking, from its own sessions or the client's
// device token, and says so in [plugin.Caller]; this file never reads an
// identity out of a header. The client's own calls — a lap posted, its lines
// fetched, a stint's debrief read — are the driver's, on the same terms.

// The host asks whether a plugin serves by asserting this interface, so a
// method that drifts from it is not a compile error — it is every route
// answering "this plugin does not serve a route" at runtime.
var _ plugin.Server = (*Engineer)(nil)

// MaxAgentBytes is the largest persona this plugin will keep.
//
// It is far above anything a person writes and far below what the host would
// carry — the point is not to save space, it is that a persona is a page of
// prose and a megabyte of it is somebody pasting the wrong file.
const MaxAgentBytes = 32 << 10

// ServeHTTP answers the routes the manifest declares.
//
// Three pages for people — the operator's overview and a driver's own, and the
// prompt — and three answers for programs: a client posts a lap and gets the
// lines, fetches a lap's lines again, or reads the debrief for a stint.
func (e *Engineer) ServeHTTP(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	switch {
	case r.Path == "/" && r.Method == http.MethodGet:
		return e.pageOverview(ctx, r)
	case strings.HasPrefix(r.Path, "/drivers/") && r.Method == http.MethodGet:
		return e.pageDriver(ctx, r, strings.TrimPrefix(r.Path, "/drivers/"))
	case r.Path == "/me" && r.Method == http.MethodGet:
		return e.pageMine(ctx, r)
	case r.Path == "/agent" && r.Method == http.MethodGet:
		return e.pageAgent(ctx, r, "")
	case r.Path == "/agent" && r.Method == http.MethodPost:
		return e.postAgent(ctx, r)
	case r.Path == "/laps" && r.Method == http.MethodPost:
		return e.postLap(ctx, r)
	case r.Path == "/cues" && r.Method == http.MethodGet:
		return e.getCues(ctx, r)
	case r.Path == "/debrief" && r.Method == http.MethodGet:
		return e.getDebrief(ctx, r)
	default:
		return plugin.Text(http.StatusNotFound, "There is nothing at that address."), nil
	}
}

// pageMine is the driver's own, and only their own: the coach's reading of
// them if one has been written, what it said lap by lap, and their debriefs.
func (e *Engineer) pageMine(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	// The slug comes from the host's session. A driver cannot ask for somebody
	// else's by changing a parameter, because there is no parameter.
	if !r.Caller.SignedIn() {
		return plugin.Text(http.StatusUnauthorized, "Sign in to read your coaching."), nil
	}
	if e.Store == nil {
		return plugin.HTML(http.StatusOK, page("Your coaching",
			`  <div class="card"><p class="muted">Nothing has been kept yet.</p></div>`)), nil
	}
	slug := r.Caller.DriverSlug

	var b strings.Builder
	// The reading is written when an administrator opens the driver's page,
	// not here: it is the operator's money, so it is the operator's click.
	if p, err := e.Store.Profile(ctx, slug); err == nil {
		b.WriteString(profileCard(p, false))
	}
	if cues, err := e.Store.RecentLapCues(ctx, slug, 40); err == nil && len(cues) > 0 {
		b.WriteString(cueList(cues))
	}
	list, err := e.Store.Recent(ctx, slug, 50)
	if err != nil {
		b.WriteString(`  <div class="card"><p class="warn">Your debriefs could not be read just now.</p></div>
`)
	} else {
		b.WriteString(debriefList(list, false))
	}
	return plugin.HTML(http.StatusOK, page("Your coaching", b.String())), nil
}

// pageAgent shows every part of the prompt, each editable on its own.
//
// One part per box rather than one box for all of it: an operator changing how
// the debrief reads should not have to scroll past the cue rules, and a part
// they have not touched should visibly be the built-in one rather than a copy
// of it they now own.
func (e *Engineer) pageAgent(ctx context.Context, r plugin.HTTPRequest, note string) (plugin.HTTPResponse, error) {
	written := map[Part]Written{}
	if e.Store != nil {
		if got, err := e.Store.Prompts(ctx); err == nil {
			written = got
		}
	}

	var b strings.Builder
	if note != "" {
		fmt.Fprintf(&b, `  <div class="notice good">%s</div>
`, esc(note))
	}
	b.WriteString(`  <div class="card">
    <p class="muted">This is what the coach is asked, in the order it is assembled: who it is, then
how each job is asked for. Change any part; the rest stay as the binary ships them.</p>
    <p class="warn">What the coach may <em>say</em> is not here. The word limits, the one-sentence
rule, how units are spelled and the requirement that every turn it names is a corner the detector
actually found are checked in code after the model answers, whatever these ask for. Asking for more
than the validator allows does not change what a driver hears — it produces cues that are thrown
away.</p>
  </div>
`)

	for _, info := range Parts() {
		text, source := info.BuiltIn, "the built-in one"
		if w, ok := written[info.Part]; ok && strings.TrimSpace(w.Markdown) != "" {
			text = w.Markdown
			source = fmt.Sprintf("changed %s by %s (%s)",
				w.UpdatedAt.Format(time.RFC822), plainOr(w.UpdatedBy, "an administrator"), w.SHA256[:8])
		}
		_, edited := written[info.Part]

		fmt.Fprintf(&b, `  <div class="card">
    <h2>%s</h2>
    <p class="about">%s</p>
    <p class="faint">In use: %s</p>
    <form method="post" action="%s" enctype="multipart/form-data">
      <input type="hidden" name="part" value="%s">
      <p><textarea name="markdown" rows="%d" spellcheck="false">%s</textarea></p>
      <div class="actions">
        <button class="btn" type="submit">Save</button>`,
			esc(info.Title), esc(info.About), esc(source),
			esc(r.Prefix+"/agent"), esc(string(info.Part)), info.Rows, esc(text))
		if edited {
			b.WriteString(`
        <button class="ghost" type="submit" name="clear" value="1">Back to the built-in one</button>`)
		}
		b.WriteString(`
        <label class="file">or attach a file <input type="file" name="file" accept=".md,text/markdown,text/plain"></label>
      </div>
    </form>
  </div>
`)
	}
	return plugin.HTML(http.StatusOK, page("What the coach is asked", b.String())), nil
}

// postAgent saves one part, or puts one back.
func (e *Engineer) postAgent(ctx context.Context, r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	if e.Store == nil {
		return plugin.Text(http.StatusServiceUnavailable,
			"This plugin has no database, so there is nowhere to keep a prompt."), nil
	}

	body, err := fields(r)
	if err != nil {
		return e.pageAgent(ctx, r, "That form could not be read.")
	}

	// The part is named by the form rather than guessed, and one the binary
	// does not have is refused: a row nothing reads is a row that looks saved.
	if _, ok := builtIn(body.part); !ok {
		return e.pageAgent(ctx, r, "That is not a part of the prompt.")
	}

	if body.clear {
		if err = e.Store.ClearPart(ctx, body.part); err != nil {
			return e.pageAgent(ctx, r, "It could not be put back just now.")
		}
		e.forget()
		e.Log.LogAttrs(ctx, slog.LevelInfo, "a part of the prompt was put back to the built-in one",
			slog.String("part", string(body.part)), slog.String("by", r.Caller.AdminEmail))
		return e.pageAgent(ctx, r, "That part is the built-in one again.")
	}

	switch {
	case strings.TrimSpace(body.markdown) == "":
		return e.pageAgent(ctx, r, "That is empty, and an empty part is not one.")
	case len(body.markdown) > MaxAgentBytes:
		return e.pageAgent(ctx, r, fmt.Sprintf(
			"That is %d bytes and the limit is %d. A prompt is prose, not a document.",
			len(body.markdown), MaxAgentBytes))
	case !isText(body.markdown):
		return e.pageAgent(ctx, r, "That does not look like text.")
	}

	w, err := e.Store.SavePart(ctx, body.part, body.markdown, r.Caller.AdminEmail)
	if err != nil {
		return e.pageAgent(ctx, r, "It could not be saved just now.")
	}
	e.forget()
	e.Log.LogAttrs(ctx, slog.LevelInfo, "a part of the prompt was changed",
		slog.String("part", string(body.part)),
		slog.String("by", r.Caller.AdminEmail), slog.String("sha256", w.SHA256[:8]))
	return e.pageAgent(ctx, r, "Saved. Answers from now on are stamped with the new prompt.")
}

// upload is what a POST to /agent carried.
type upload struct {
	// part is which piece of the prompt the form was for.
	part     Part
	markdown string
	clear    bool
}

// fields reads the multipart body the form sends.
//
// It is parsed here rather than with net/http because a plugin is handed the
// bytes and the headers, not a *http.Request — the host read the body, capped
// it and passed it on, which is the whole point of the boundary.
func fields(r plugin.HTTPRequest) (upload, error) {
	ct := r.Header.Get("Content-Type")
	kind, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return upload{}, fmt.Errorf("the content type is not one: %w", err)
	}

	// The button that puts the built-in persona back posts the same form, so
	// its name arrives as an ordinary field either way.
	if kind == "application/x-www-form-urlencoded" {
		values, parseErr := url.ParseQuery(string(r.Body))
		if parseErr != nil {
			return upload{}, parseErr
		}
		return upload{
			part:     Part(values.Get("part")),
			markdown: values.Get("markdown"),
			clear:    values.Get("clear") != "",
		}, nil
	}
	if !strings.HasPrefix(kind, "multipart/") {
		return upload{}, fmt.Errorf("%s is not a form", kind)
	}

	mr := multipart.NewReader(strings.NewReader(string(r.Body)), params["boundary"])
	form, err := mr.ReadForm(MaxAgentBytes)
	if err != nil {
		return upload{}, err
	}
	defer func() { _ = form.RemoveAll() }()

	out := upload{}
	if v := form.Value["part"]; len(v) > 0 {
		out.part = Part(v[0])
	}
	if v := form.Value["clear"]; len(v) > 0 && v[0] != "" {
		out.clear = true
		return out, nil
	}
	// A file if one was attached, and what was typed otherwise.
	files := form.File["file"]
	if len(files) == 0 || files[0].Size == 0 {
		if v := form.Value["markdown"]; len(v) > 0 {
			out.markdown = v[0]
		}
		return out, nil
	}
	f, err := files[0].Open()
	if err != nil {
		return upload{}, err
	}
	defer func() { _ = f.Close() }()

	var b strings.Builder
	if _, err := io.CopyN(&b, f, MaxAgentBytes+1); err != nil && !errors.Is(err, io.EOF) {
		return upload{}, err
	}
	out.markdown = b.String()
	return out, nil
}

// isText refuses a file that is not one. A persona is prose, and the most
// likely wrong file is a binary somebody picked by mistake — which would be
// stored, sent to the vendor and paid for before anybody noticed.
func isText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == 0 || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			return false
		}
	}
	return true
}

// page wraps a body in the server's own shell.
//
// A plugin's page is on the operator's domain, one click from the panel, and
// looking like a different program is how it reads as one. So it links the
// host's stylesheet — served at /assets/app.css to anyone — and uses the
// classes the panel uses: a card, a heading, muted text, a pill.
//
// That is a dependency on somebody else's CSS and it is worth being honest
// about: if the panel renames a class, this looks wrong until it is updated.
// The alternative is a plugin that invents its own look on the operator's
// domain, which is worse every day rather than on the day of an upgrade. What
// the page does not do is depend on the panel's markup — no navigation, no
// header — because those belong to the panel and a plugin is not it.
func page(title, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s — engineer</title>
<link rel="stylesheet" href="/assets/app.css">
<style>
 /* The few things the panel has no class for, because it has no page like
    this one: a prose field, and a block of text the coach wrote. */
 textarea {
   width: 100%%; box-sizing: border-box; background: var(--inputbg);
   border: 1px solid var(--border-strong); color: var(--text);
   padding: 10px 12px; font: 13px/1.6 var(--mono); resize: vertical;
   clip-path: var(--chamfer-sm);
 }
 .about { color: var(--muted); margin: 0 0 10px; }
 .said { margin: 0 0 10px; }
 .drill { color: var(--muted); }
 .file { color: var(--faint); font-size: 12px; margin-left: 10px; }
 .card + .card { margin-top: 14px; }
</style>
</head>
<body>
<div class="wrap wide">
  <header class="top">
    <span class="name">engineer</span>
    <span class="wm">plugin</span>
    <span class="spacer"></span>
    <a class="ghost" href="/admin/plugins/engineer">Back to the panel</a>
  </header>
  <h1>%s</h1>
`, esc(title), esc(title))
	b.WriteString(body)
	b.WriteString(`
  <footer class="foot">Served by the engineer plugin, not by the server.</footer>
</div>
</body>
</html>`)
	return b.String()
}

// debriefList renders what was written. withDriver is false on a driver's own
// page, where every one of them is theirs and saying so each time is noise.
func debriefList(list []Debrief, withDriver bool) string {
	if len(list) == 0 {
		return `  <div class="card">
    <h2>Nothing yet</h2>
    <p class="muted">A debrief is written when a stint finishes, if the operator asked for one.</p>
  </div>
`
	}
	var b strings.Builder
	for i := range list {
		d := &list[i]
		b.WriteString(`  <div class="card">
`)
		who := ""
		if withDriver {
			who = esc(d.DriverName) + " — "
		}
		fmt.Fprintf(&b, `    <p class="faint">%s%s · %s · %s</p>
`,
			who, esc(d.WrittenAt.Format(time.RFC822)), esc(plainOr(d.Track, "an unnamed circuit")), esc(d.Car))
		fmt.Fprintf(&b, `    <p class="said">%s</p>
`, esc(d.Summary))
		// Observation and drill stay apart on the page as they do in the
		// record: one is what the data showed, the other is the thing to
		// practise, and a driver acts on the second.
		if len(d.Findings) > 0 {
			b.WriteString(`    <table class="facts rows">
`)
			for _, f := range d.Findings {
				fmt.Fprintf(&b, `      <tr><th>%s</th><td>%s<div class="drill">%s</div></td></tr>
`,
					esc(plainOr(f.Area, "in general")), esc(f.Observation), esc(f.Drill))
			}
			b.WriteString(`    </table>
`)
		}
		// The car, apart from the driving: a control, the change, and the
		// measurement that says why.
		if len(d.Setup) > 0 {
			b.WriteString(`    <p class="faint">Setup</p>
    <table class="facts rows">
`)
			for _, c := range d.Setup {
				fmt.Fprintf(&b, `      <tr><th>%s</th><td>%s<div class="drill">%s</div></td></tr>
`,
					esc(c.Setting), esc(c.fromTo()), esc(c.Because))
			}
			b.WriteString(`    </table>
`)
		}
		b.WriteString(`  </div>
`)
	}
	return b.String()
}

// cueList renders what was said lap by lap, newest first: each corner's line,
// the radio line, and what the lap noted about the car.
func cueList(rows []LapCues) string {
	var b strings.Builder
	b.WriteString(`  <div class="card">
    <h2>Lap by lap</h2>
    <p class="muted">What the coach said, for the corner ahead in practice and on the radio in a race.</p>
    <table class="facts rows">
`)
	for i := range rows {
		c := &rows[i]
		fmt.Fprintf(&b, `      <tr><th>Lap %d<div class="faint">%s</div></th><td>`, c.Lap, esc(c.WrittenAt.Format(time.RFC822)))
		for _, line := range c.Cues {
			if line.Turn == 0 {
				fmt.Fprintf(&b, `<div>Radio: %s</div>`, esc(line.Line))
			} else {
				fmt.Fprintf(&b, `<div>Turn %d: %s</div>`, line.Turn, esc(line.Line))
			}
		}
		for _, n := range c.SetupNotes {
			fmt.Fprintf(&b, `<div class="drill">About the car: %s</div>`, esc(n))
		}
		b.WriteString(`</td></tr>
`)
	}
	b.WriteString(`    </table>
  </div>
`)
	return b.String()
}

// esc is the one rule about anything that came from outside this binary: a
// driver's name, a track, a line the model wrote, a file an operator uploaded.
func esc(s string) string { return html.EscapeString(s) }

// plainOr is a value or a word for not having one.
func plainOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
