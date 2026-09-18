package commands

import (
	"context"
	"os"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// cliActor identifies the local operator running the command.
func cliActor() audit.Actor {
	label := os.Getenv("USER")
	if label == "" {
		label = os.Getenv("LOGNAME")
	}
	if label == "" {
		label = "cli"
	}
	return audit.Actor{Type: audit.ActorCLI, Label: label}
}

// cliAudit returns a stderr-backed audit logger and a context carrying
// the CLI actor, so deployer and command events are attributed.
func cliAudit(ctx context.Context, st *store.Store) (context.Context, *audit.Logger) {
	return audit.WithActor(ctx, cliActor()), audit.New(st, os.Stderr)
}
