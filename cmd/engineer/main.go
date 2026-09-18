// Command engineer is the coaching plugin: a race engineer for a team's drivers,
// corner by corner in practice, on the radio in a race, and in writing after.
//
// It is started by the Pacenote server, not by a person. Running it from a shell
// prints the handshake line and exits, which is what go-plugin does when nothing
// is on the other end.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/pacenote-sim/plugin"

	"github.com/pacenote-sim/engineer"
)

// openTimeout bounds the one thing this program does before it starts serving.
// A database that will not answer is a plugin that should fail loudly now rather
// than at the first lap of somebody's race.
const openTimeout = 10 * time.Second

func main() {
	// Standard error, because standard output is the handshake and one line on
	// it breaks the connection. The host captures this, scrubs it of
	// credentials and shows it in the panel.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	store, err := openStore(log)
	if err != nil {
		// Fatal on purpose. This plugin declared a database in its manifest, so
		// one was created for it; carrying on without it would mean writing
		// debriefs nobody can read back and failing silently for a season.
		fmt.Fprintf(os.Stderr, "engineer: %v\n", err)
		os.Exit(1)
	}
	if store != nil {
		defer store.Close()
	}

	plugin.Serve(engineer.New(store, log))
}

// openStore connects to the database the host handed over.
func openStore(log *slog.Logger) (*engineer.Store, error) {
	dsn, err := plugin.DatabaseURL()
	if err != nil {
		// A host too old to provide one, or a manifest edited to remove the
		// declaration. The coach still works — it just does not keep what it
		// wrote — so this is a line in the log rather than an exit.
		log.Warn("no database was provided, so debriefs will not be kept", "reason", err)
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	return engineer.Open(ctx, dsn)
}
