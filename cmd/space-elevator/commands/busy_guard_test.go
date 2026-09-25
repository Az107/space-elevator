package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/store"
)

func openGuardTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "space-elevator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestRefuseIfBusyIdleApp guards the exact regression that broke seven CLI
// commands: an app with no operation in flight must be allowed through.
// Store.ActiveOperation reports "idle" as store.ErrNotFound, so a guard that
// treats any error as "busy" rejects every healthy app.
func TestRefuseIfBusyIdleApp(t *testing.T) {
	ctx := context.Background()
	st := openGuardTestStore(t)

	if err := st.CreateApp(ctx, &store.App{
		ID: "idle-app", Name: "idle-app", SourceType: "git", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	if err := refuseIfBusy(ctx, st, "idle-app"); err != nil {
		t.Fatalf("idle app must not be rejected: %v", err)
	}
}

func TestRefuseIfBusyActiveApp(t *testing.T) {
	ctx := context.Background()
	st := openGuardTestStore(t)

	if err := st.CreateApp(ctx, &store.App{
		ID: "busy-app", Name: "busy-app", SourceType: "git", Status: "updating",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAppOperation(ctx, "busy-app", "update"); err != nil {
		t.Fatal(err)
	}

	if err := refuseIfBusy(ctx, st, "busy-app"); err == nil {
		t.Fatal("app with an active operation must be rejected")
	}
}

// TestRefuseIfBusyFinishedOperation covers the boundary: a *completed*
// operation is terminal and must not block later CLI work.
func TestRefuseIfBusyFinishedOperation(t *testing.T) {
	ctx := context.Background()
	st := openGuardTestStore(t)

	if err := st.CreateApp(ctx, &store.App{
		ID: "done-app", Name: "done-app", SourceType: "git", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	op, err := st.ClaimAppOperation(ctx, "done-app", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperation(ctx, op.ID, store.OperationStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}

	if err := refuseIfBusy(ctx, st, "done-app"); err != nil {
		t.Fatalf("app with a completed operation must not be rejected: %v", err)
	}
}
