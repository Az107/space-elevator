package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/service"
)

// TestResolveUpdateTargetRefusesMismatch guards the silent no-op: running the
// updater from a binary other than the one the unit executes must not look
// like a successful update.
func TestResolveUpdateTargetRefusesMismatch(t *testing.T) {
	self, err := service.SelfBinary()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	unitDir := filepath.Join(dir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(unitDir, "space-elevator.service")

	// A unit that runs a different binary must produce a mismatch.
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/somewhere/else/space-elevator serve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, mismatch, err := resolveUpdateTarget()
	if err != nil {
		t.Fatal(err)
	}
	if mismatch == "" {
		t.Fatal("expected a mismatch when the unit runs a different binary")
	}

	// A unit that runs this executable must not.
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart="+self+" serve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, mismatch, err := resolveUpdateTarget()
	if err != nil {
		t.Fatal(err)
	}
	if mismatch != "" {
		t.Fatalf("unexpected mismatch for matching unit: %s", mismatch)
	}
	if target != self {
		t.Fatalf("target = %q, want %q", target, self)
	}
}

// TestResolveUpdateTargetHonorsFlag checks that --target bypasses the guard.
func TestResolveUpdateTargetHonorsFlag(t *testing.T) {
	old := updateTarget
	t.Cleanup(func() { updateTarget = old })

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	updateTarget = filepath.Join(dir, "explicit-binary")
	target, mismatch, err := resolveUpdateTarget()
	if err != nil {
		t.Fatal(err)
	}
	if mismatch != "" {
		t.Fatalf("unexpected mismatch with --target: %s", mismatch)
	}
	if target != updateTarget {
		t.Fatalf("target = %q, want %q", target, updateTarget)
	}
}
