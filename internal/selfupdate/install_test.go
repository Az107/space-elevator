package selfupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTargetFromUnit(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "space-elevator.service")
	body := "[Service]\n" +
		"ExecStart=/home/services/.local/bin/space-elevator serve\n" +
		"Restart=always\n"
	if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TargetFromUnit(unit)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/services/.local/bin/space-elevator" {
		t.Fatalf("TargetFromUnit = %q", got)
	}

	// systemd "-" prefix and quotes.
	body2 := "[Service]\nExecStart=\"-/opt/se/space-elevator\" serve --addr x\n"
	if err := os.WriteFile(unit, []byte(body2), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := TargetFromUnit(unit); err != nil || got != "/opt/se/space-elevator" {
		t.Fatalf("TargetFromUnit = (%q,%v)", got, err)
	}

	// Legacy units use %h; expand it so the target matches the real path.
	home, _ := os.UserHomeDir()
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=%h/.local/bin/space-elevator serve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := TargetFromUnit(unit); err != nil || got != filepath.Join(home, ".local/bin/space-elevator") {
		t.Fatalf("TargetFromUnit(%%h) = (%q,%v)", got, err)
	}
}

func TestPreflightRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Target: link}
	if err := in.Preflight(); err == nil {
		t.Fatal("Preflight on symlink = nil error, want error")
	}
}

func TestStageThenCommitKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "space-elevator")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Target: target}
	if err := in.Preflight(); err != nil {
		t.Fatal(err)
	}

	payload := []byte("new-binary-content")
	tmp, sha, err := in.Stage(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	// Stage must not touch the target.
	if b, _ := os.ReadFile(target); !bytes.Equal(b, []byte("old-binary")) {
		t.Fatalf("target modified by Stage: %q", b)
	}
	if want, _ := FileSHA(tmp); sha != want {
		t.Fatalf("Stage sha = %q, want %q", sha, want)
	}

	if err := in.Commit(tmp, sha); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, payload) {
		t.Fatalf("target after Commit = %q", b)
	}
	if b, _ := os.ReadFile(target + ".prev"); !bytes.Equal(b, []byte("old-binary")) {
		t.Fatalf(".prev = %q, want old-binary", b)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %v (%v), want 0755", info.Mode(), err)
	}
}

func TestCommitRejectsBadChecksum(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "space-elevator")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Target: target}
	tmp, _, err := in.Stage(bytes.NewReader([]byte("new")))
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Commit(tmp, "deadbeef"); err == nil {
		t.Fatal("Commit with wrong checksum = nil error, want error")
	}
}

func TestRestore(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "space-elevator")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Target: target}
	tmp, sha, err := in.Stage(bytes.NewReader([]byte("new")))
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Commit(tmp, sha); err != nil {
		t.Fatal(err)
	}
	if !in.HasPrevious() {
		t.Fatal("HasPrevious = false after Commit")
	}
	if err := in.Restore(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, []byte("old")) {
		t.Fatalf("after Restore = %q, want old", b)
	}
}

func TestRestoreWithoutPrevious(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "space-elevator")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Target: target}
	if in.HasPrevious() {
		t.Fatal("HasPrevious = true with no .prev")
	}
	if err := in.Restore(); err == nil {
		t.Fatal("Restore without .prev = nil error, want error")
	}
}

func TestSmokeTest(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("#!/bin/sh\necho 'space-elevator version v9.9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SmokeTest(good); err != nil {
		t.Fatalf("SmokeTest(good) = %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SmokeTest(empty); err == nil {
		t.Fatal("SmokeTest(empty) = nil error, want error")
	}
}

func TestProbeURL(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8080":   "http://127.0.0.1:8080/",
		"127.0.0.1:8080": "http://127.0.0.1:8080/",
		"":               "",
		"nonsense":       "",
	}
	for in, want := range cases {
		if got := ProbeURL(in); got != want {
			t.Errorf("ProbeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
