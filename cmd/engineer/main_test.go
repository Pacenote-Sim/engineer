package main

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// A plugin the host gave no database still coaches. It just does not keep what
// it wrote, which is a line in the log rather than a reason to refuse to start:
// the cues are the point and the debriefs are the record.
func TestNoDatabaseIsALineInTheLogAndNotAnExit(t *testing.T) {
	r := require.New(t)

	t.Setenv(plugin.EnvDatabaseURL, "")

	var buf bytes.Buffer
	store, err := openStore(slog.New(slog.NewTextHandler(&buf, nil)))
	r.NoError(err, "a plugin with no database refused to start")
	r.Nil(store)
	r.Contains(buf.String(), "no database was provided")
}

// A database that was provided and will not open is fatal, because this plugin
// declared one in its manifest: carrying on would mean writing debriefs nobody
// can read back and failing silently for a season.
func TestADatabaseThatWillNotOpen(t *testing.T) {
	r := require.New(t)

	t.Setenv(plugin.EnvDatabaseURL,
		"postgres://nobody:nothing@127.0.0.1:1/pacenote?sslmode=disable&connect_timeout=1")

	var buf bytes.Buffer
	_, err := openStore(slog.New(slog.NewTextHandler(&buf, nil)))
	r.Error(err)
}
