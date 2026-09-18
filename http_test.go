package engineer

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// The pages, without a database.
//
// What is asserted here is the boundary rather than the prose: which addresses
// exist, who the pages are for, and that nothing from outside this binary
// reaches the page unescaped. The store is covered against a real PostgreSQL in
// store_postgres_test.go.

func TestTheAddressesThisPluginServes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := New(nil, nil)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/agent", http.StatusOK},
		{http.MethodGet, "/drivers/mihai", http.StatusOK},
		{http.MethodGet, "/nowhere", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusNotFound},
		{http.MethodDelete, "/agent", http.StatusNotFound},
		// The client's own addresses want a signed-in driver, and the host
		// forwards nobody to them; a stranger who reaches them anyway is refused.
		{http.MethodPost, "/laps", http.StatusUnauthorized},
		{http.MethodGet, "/laps", http.StatusNotFound},
		{http.MethodGet, "/cues", http.StatusUnauthorized},
		{http.MethodGet, "/debrief", http.StatusUnauthorized},
		{http.MethodGet, "/me", http.StatusUnauthorized},
	} {
		res, err := e.ServeHTTP(context.Background(), plugin.HTTPRequest{Method: tc.method, Path: tc.path})
		r.NoError(err)
		r.Equalf(tc.want, res.Status, "%s %s", tc.method, tc.path)
	}
}

// A driver's page is their own. There is no parameter naming a driver, which is
// the point: the slug comes from the host's session and cannot be asked for.
func TestADriverReadsOnlyTheirOwn(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := New(nil, nil)

	res, err := e.ServeHTTP(context.Background(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/me"})
	r.NoError(err)
	r.Equal(http.StatusUnauthorized, res.Status, "a stranger was shown a driver's page")

	res, err = e.ServeHTTP(context.Background(), plugin.HTTPRequest{
		Method: http.MethodGet, Path: "/me",
		Caller: plugin.Caller{DriverSlug: "ana-lopez", DriverName: "Ana López"},
	})
	r.NoError(err)
	r.Equal(http.StatusOK, res.Status)
	r.Contains(string(res.Body), "Your coaching")
}

// The client's questions need a stint to be about, and a database to answer
// from. Each refusal says what is missing, because the client's author reads it.
func TestTheClientsQuestionsNeedAStintAndADatabase(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := New(nil, nil)
	ask := func(path, query string) plugin.HTTPResponse {
		res, err := e.ServeHTTP(context.Background(), plugin.HTTPRequest{
			Method: http.MethodGet, Path: path, Query: query, Caller: plugin.Caller{DriverSlug: "mihai"},
		})
		r.NoError(err)
		return res
	}

	r.Equal(http.StatusBadRequest, ask("/cues", "").Status)
	r.Contains(string(ask("/cues", "").Body), "?stint=")
	r.Equal(http.StatusBadRequest, ask("/cues", "stint=s&lap=zero").Status)
	r.Equal(http.StatusBadRequest, ask("/cues", "stint=s&lap=0").Status)
	r.Equal(http.StatusBadRequest, ask("/cues", "stint=two%20words").Status)
	r.Equal(http.StatusServiceUnavailable, ask("/cues", "stint=s&lap=7").Status, "without a database, the client is told so")

	r.Equal(http.StatusBadRequest, ask("/debrief", "").Status)
	r.Equal(http.StatusServiceUnavailable, ask("/debrief", "stint=s").Status)
}

// The page says what an operator may change and what they may not, because a
// persona that disagrees with the validator produces discarded cues and no
// explanation.
func TestThePromptPageSaysWhatIsNotTheirs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res, err := New(nil, nil).ServeHTTP(context.Background(),
		plugin.HTTPRequest{Method: http.MethodGet, Path: "/agent"})
	r.NoError(err)
	body := string(res.Body)
	r.Contains(body, "the built-in one")
	r.Contains(body, "checked in code after the model answers")
	r.Contains(body, "thrown away")

	// Every part is on the page, each with its own box and its own name, or a
	// save posts the wrong one.
	for _, info := range Parts() {
		r.Containsf(body, `name="part" value="`+string(info.Part)+`"`, "%s has no form", info.Part)
		r.Containsf(body, esc(info.Title), "%s has no heading", info.Part)
	}
	r.Contains(body, "race engineer", "the persona in use is not shown")
	r.Contains(body, "Write one line to be spoken aloud", "the cue rules are not shown")
}

// Anything that came from outside this binary is escaped. A driver's name is
// the easy one; the line the model wrote is the one that matters, because it is
// the only text on the page this plugin did not choose.
func TestNothingFromOutsideReachesThePageAsMarkup(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	html := debriefList([]Debrief{{
		DriverName: `<script>alert(1)</script>`,
		Track:      `Spa <b>`,
		Car:        `GT3 " onload="x`,
		Summary:    `You were quick <img src=x onerror=y>`,
		Findings:   []Finding{{Area: "<i>", Observation: "<u>", Drill: `</article><script>`}},
		Setup:      []SetupChange{{Setting: "<b>Rear wing</b>", From: "7", To: `6" onload="x`, Because: "<script>"}},
	}}, true)

	r.NotContains(html, "<script>")
	r.NotContains(html, "<img")
	r.NotContains(html, `onload="`)
	r.NotContains(html, "<b>")
	r.Contains(html, "&lt;script&gt;", "it was not escaped, it was dropped")
	r.Contains(html, "7 to 6", "the setup change is not on the page")

	// And the lap-by-lap list, whose lines the model wrote.
	laps := cueList([]LapCues{{
		Lap:        7,
		Cues:       []cueLine{{Turn: 4, Line: "<script>alert(1)</script>"}, {Line: "P4 <img src=x>"}},
		SetupNotes: []string{"<u>"},
	}})
	r.NotContains(laps, "<script>")
	r.NotContains(laps, "<img")
	r.Contains(laps, "Turn 4: &lt;script&gt;")
	r.Contains(laps, "Radio: P4")
	r.Contains(laps, "About the car: &lt;u&gt;")
}

// A file that is not prose is refused before it is stored, sent to a vendor and
// paid for.
func TestWhatIsNotAPersona(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isText("# The coach\n\nBe blunt.\tUse metres.\r\n"))
	r.False(isText("\x00\x01\x02binary"), "a binary was taken for prose")
	r.False(isText(string([]byte{0xff, 0xfe, 0x00})), "invalid utf-8 was taken for prose")
	r.False(isText("a\x07b"), "a control character was taken for prose")
}

// The form both buttons post.
func TestReadingTheUploadedForm(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body, ct := formFields(t, map[string]string{"part": "persona"}, "agent.md", "# The coach\n\nBe blunt.")
	got, err := fields(plugin.HTTPRequest{
		Header: http.Header{"Content-Type": {ct}}, Body: body,
	})
	r.NoError(err)
	r.Equal("# The coach\n\nBe blunt.", got.markdown)
	r.False(got.clear)

	// The other button, posted as an ordinary form.
	got, err = fields(plugin.HTTPRequest{
		Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		Body:   []byte("part=persona&clear=1"),
	})
	r.NoError(err)
	r.True(got.clear)

	_, err = fields(plugin.HTTPRequest{Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte("{}")})
	r.Error(err, "a body that is not a form was read as one")
}

// Without a database there is nowhere to keep a persona, and the page says so
// rather than accepting one and losing it.
func TestUploadingAPersonaWithNowhereToKeepIt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body, ct := formFields(t, map[string]string{"part": "persona"}, "agent.md", "# The coach")
	res, err := New(nil, nil).ServeHTTP(context.Background(), plugin.HTTPRequest{
		Method: http.MethodPost, Path: "/agent",
		Header: http.Header{"Content-Type": {ct}}, Body: body,
	})
	r.NoError(err)
	r.Equal(http.StatusServiceUnavailable, res.Status)
}

// With nothing written, every part is the built-in one and the stamp is the
// bare version.
func TestThePromptWithNothingWritten(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	parts, version := New(nil, nil).prompt(context.Background())
	r.Equal(PromptVersion, version)
	r.Len(parts, len(Parts()))
	for _, info := range Parts() {
		r.Equalf(info.BuiltIn, parts[info.Part], "%s is not the built-in one", info.Part)
		r.NotEmptyf(parts[info.Part], "%s is empty", info.Part)
	}
}

// Every part a page offers has to be one the binary knows, or saving it writes
// a row nothing ever reads.
func TestEveryPartOfferedIsOneTheBinaryHas(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	seen := map[Part]bool{}
	for _, info := range Parts() {
		r.NotEmptyf(info.Title, "%s has no title", info.Part)
		r.NotEmptyf(info.About, "%s does not say why it would be changed", info.Part)
		r.Positivef(info.Rows, "%s has no height", info.Part)
		r.Falsef(seen[info.Part], "%s is listed twice", info.Part)
		seen[info.Part] = true

		text, ok := builtIn(info.Part)
		r.Truef(ok, "%s is offered and the binary has no text for it", info.Part)
		r.Equal(info.BuiltIn, text)
	}
	_, ok := builtIn(Part("invented"))
	r.False(ok)
}

// The persona can be edited in the page as well as attached as a file. Both
// arrive through the same form, so both are read here.
func TestThePersonaCanBeEditedOrAttached(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Typed into the textarea and posted as an ordinary form.
	got, err := fields(plugin.HTTPRequest{
		Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		Body:   []byte("part=persona&markdown=" + url.QueryEscape("# Edited\n\nBe blunt.")),
	})
	r.NoError(err)
	r.Equal("# Edited\n\nBe blunt.", got.markdown)
	r.False(got.clear)

	// Typed into the textarea, posted as multipart because the form carries a
	// file input whether or not a file was chosen.
	body, ct := formFields(t, map[string]string{"part": "cue", "markdown": "# Typed"}, "", "")
	got, err = fields(plugin.HTTPRequest{Header: http.Header{"Content-Type": {ct}}, Body: body})
	r.NoError(err)
	r.Equal("# Typed", got.markdown)

	// Both given: the attached file is the more deliberate act and wins.
	body, ct = formFields(t, map[string]string{"part": "cue", "markdown": "# Typed"}, "agent.md", "# Attached")
	got, err = fields(plugin.HTTPRequest{Header: http.Header{"Content-Type": {ct}}, Body: body})
	r.NoError(err)
	r.Equal("# Attached", got.markdown)

	// Clearing wins over both, because it is the only one that is unambiguous.
	body, ct = formFields(t, map[string]string{"part": "cue", "markdown": "# Typed", "clear": "1"}, "", "")
	got, err = fields(plugin.HTTPRequest{Header: http.Header{"Content-Type": {ct}}, Body: body})
	r.NoError(err)
	r.True(got.clear)
}

// The page hands back what is in use so it can be edited, and a persona holding
// markup does not break the form it is edited in.
func TestThePromptPageIsEditable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res, err := New(nil, nil).ServeHTTP(context.Background(),
		plugin.HTTPRequest{Method: http.MethodGet, Path: "/agent"})
	r.NoError(err)
	body := string(res.Body)
	r.Contains(body, `name="markdown"`, "there is nothing to edit it in")
	r.Equal(len(Parts()), strings.Count(body, "<textarea"), "not one box per part")
	r.Contains(body, "race engineer", "a textarea is empty rather than holding what is in use")

	// A persona that contains a closing tag must not end the textarea early.
	html := page("x", `<textarea>`+esc(`</textarea><script>alert(1)</script>`)+`</textarea>`)
	r.NotContains(html, "<script>")
	r.Contains(html, "&lt;/textarea&gt;")
}

// formFields builds a multipart body with values and optionally one file.
func formFields(t *testing.T, values map[string]string, filename, content string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range values {
		require.NoError(t, w.WriteField(k, v))
	}
	if filename != "" {
		part, err := w.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = strings.NewReader(content).WriteTo(part)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return b.Bytes(), w.FormDataContentType()
}

// A form has to post back to where the page came from.
//
// HTTPRequest carries two addresses and they are not interchangeable. Prefix is
// where this plugin is mounted on the server the caller actually reached;
// BaseURL is the operator's configured public address, for a link sent
// somewhere else — a webhook address handed to a provider.
//
// Using BaseURL for a form action breaks the page whenever the two differ, and
// they differ constantly: a server configured with a public host of "localhost"
// behind a proxy builds "https://localhost", while the operator is reading the
// page on "http://localhost:8080". The button then posts to another scheme and
// another port, and nothing happens at all.
func TestAFormPostsBackToWhereThePageCameFrom(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res, err := New(nil, nil).ServeHTTP(context.Background(), plugin.HTTPRequest{
		Method:  http.MethodGet,
		Path:    "/agent",
		Prefix:  "/plugin/engineer",
		BaseURL: "https://somewhere-else.example.com",
	})
	r.NoError(err)
	body := string(res.Body)

	r.Contains(body, `action="/plugin/engineer/agent"`,
		"a form does not post back to where this plugin is mounted")
	r.NotContains(body, "somewhere-else.example.com",
		"a form posts to the configured public address instead of the address it was reached on")
}
