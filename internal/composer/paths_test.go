package composer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBuildContextRejectsEscape(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBuildContext(source, "../outside"); err == nil {
		t.Fatal("expected parent traversal to be rejected")
	}
	if _, err := resolveBuildContext(source, "/etc"); err == nil {
		t.Fatal("expected absolute context to be rejected")
	}
	if got, err := resolveBuildContext(source, "app"); err != nil || got != filepath.Join(source, "app") {
		t.Fatalf("context = %q, err=%v", got, err)
	}
}

func TestContainedPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := containedPath(root, filepath.Join(root, "link")); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
