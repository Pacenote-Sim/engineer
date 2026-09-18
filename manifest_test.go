package engineer_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// The manifest, loaded the way the host loads it.
//
// It uses plugin.LoadManifest rather than reading the file, because the thing
// most likely to be wrong is not the contents — it is the name. The host reads
// plugin.ManifestName and nothing else; a manifest called anything else is a
// directory the server walks past without a word, and the first sign is a
// plugin that never appears in the panel. This shipped as manifest.json.
func TestTheHostCanLoadThisManifest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	m, err := plugin.LoadManifest(".")
	r.NoError(err, "the host would not find or read this plugin's manifest")
	r.NoError(m.Validate())

	// The name is the directory the operator drops this in, and the binary is
	// what the host starts. Both are how a plugin is found rather than
	// descriptions of it.
	r.Equal("engineer", m.Name)
	r.Equal("engineer", m.Binary)
	r.Equal(plugin.InterfaceVersion, m.InterfaceVersion,
		"built against a different contract than this module compiles against")

	// What it asks for has to match what it does.
	r.True(m.Capabilities.Database, "it keeps what it wrote and declares no database")
	r.True(m.Capabilities.Network, "it calls a vendor and does not say so")
	r.Equal([]string{"api.anthropic.com"}, m.Capabilities.Calls, "an operator is owed the name of what their data is sent to")
	r.Contains(m.Description, "Anthropic")
	r.True(m.Capabilities.Serves(), "it serves pages and declares no route")
	r.True(m.Capabilities.Wants(plugin.EventLapCompleted), "the radio needs every race lap and does not ask for them")
	r.True(m.Capabilities.Wants(plugin.EventStintFinished), "the debrief needs the finished stint and does not ask for it")
	r.Empty(m.Capabilities.Requests, "nothing asks this plugin anything; a client posts to its route")
	r.Equal([]string{"voice"}, m.Capabilities.Asks, "it asks voice for every line's audio and does not say so")
	r.True(m.Capabilities.MayAsk("voice"))

	for path, want := range map[string]plugin.Access{
		"/":        plugin.AccessAdmin,
		"/agent":   plugin.AccessAdmin,
		"/me":      plugin.AccessDriver,
		"/laps":    plugin.AccessDriver,
		"/cues":    plugin.AccessDriver,
		"/debrief": plugin.AccessDriver,
	} {
		got, ok := m.Capabilities.HTTP.For(path)
		r.Truef(ok, "%s is served and not declared", path)
		r.Equalf(want, got, "%s is declared for the wrong caller", path)
	}

	// "/" is a catch-all, and that is worth being explicit about: every path
	// under this plugin that is not covered by something more specific is
	// reached by an administrator and nobody else — the driver pages included.
	// An address ServeHTTP does not handle is not refused by the host — it
	// reaches the plugin and gets a not-found from there.
	//
	// The ones that matter are that it does not swallow the driver's own
	// addresses: each is longer, so each wins.
	for path, want := range map[string]plugin.Access{
		"/anything":       plugin.AccessAdmin,
		"/drivers/mihai":  plugin.AccessAdmin,
		"/me":             plugin.AccessDriver,
		"/me/and-further": plugin.AccessDriver,
		"/laps":           plugin.AccessDriver,
		"/cues":           plugin.AccessDriver,
	} {
		got, ok := m.Capabilities.HTTP.For(path)
		r.Truef(ok, "%s reaches nothing", path)
		r.Equalf(want, got, "%s is reached by the wrong caller", path)
	}
}
