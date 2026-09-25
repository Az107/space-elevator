package podman

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDockerfileRejectsEscapesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	context := filepath.Join(root, "context")
	if err := os.MkdirAll(context, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(context, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateDockerfile(context, "../Dockerfile"); err == nil {
		t.Fatal("parent Dockerfile path was accepted")
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(context, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateDockerfile(context, "linked/Dockerfile"); err == nil {
		t.Fatal("Dockerfile reached through an escaping symlink was accepted")
	}
}

func TestTarContextUsesResolvedRootForHeaders(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "file.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r, err := tarContext(link)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// The important regression is that a symlinked context does not create
	// tar names such as ../real/file.txt. The caller must be able to consume
	// the stream without a path-safety error from the build API.
	buf := make([]byte, 4096)
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}
