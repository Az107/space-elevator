package builder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeSourceDirectory(t *testing.T) {
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<h1>hi</h1>", 0o644)
	mustWriteFile(t, filepath.Join(src, "css", "app.css"), "body{}", 0o644)

	dest := t.TempDir()
	if err := MaterializeSource(src, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "index.html")); err != nil || string(b) != "<h1>hi</h1>" {
		t.Errorf("index.html wrong: %q err=%v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "css", "app.css")); err != nil || string(b) != "body{}" {
		t.Errorf("css/app.css wrong: %q err=%v", b, err)
	}
	if kind, err := DetectKind(dest); err != nil || kind != DropKindStatic {
		t.Errorf("DetectKind = %q, %v; want static", kind, err)
	}
}

func TestMaterializeSourceArchiveDispatch(t *testing.T) {
	src := buildTarGz(t, map[string]string{"index.html": "<h1>hi</h1>"})
	dest := t.TempDir()
	if err := MaterializeSource(src, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "index.html")); err != nil || string(b) != "<h1>hi</h1>" {
		t.Errorf("archive dispatch wrong: %q err=%v", b, err)
	}
}

func TestMaterializeSourcePreservesExecBit(t *testing.T) {
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "run.sh"), "#!/bin/sh\n", 0o755)

	dest := t.TempDir()
	if err := MaterializeSource(src, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec bit not preserved: mode %v", info.Mode())
	}
}

func TestMaterializeSourceRejectsSymlink(t *testing.T) {
	src := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, target, "secret", 0o644)
	if err := os.Symlink(target, filepath.Join(src, "link.txt")); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	err := MaterializeSource(src, dest)
	if err == nil {
		t.Fatal("symlink must be rejected")
	}
	if _, statErr := os.Lstat(filepath.Join(dest, "link.txt")); !os.IsNotExist(statErr) {
		t.Error("symlink must not be copied into the drop")
	}
}

func TestCopyTreeRejectsDestinationInsideSource(t *testing.T) {
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<h1>hi</h1>", 0o644)

	dst := filepath.Join(src, "out")
	if err := MaterializeSource(src, dst); err == nil {
		t.Fatal("destination inside source must be rejected")
	}
}

func TestMaterializeSourcePerFileCap(t *testing.T) {
	src := t.TempDir()
	f, err := os.Create(filepath.Join(src, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(extractMaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := MaterializeSource(src, t.TempDir()); err == nil {
		t.Fatal("oversized source file must be rejected")
	}
}

func TestMaterializeSourceRootSymlinkResolved(t *testing.T) {
	real := t.TempDir()
	mustWriteFile(t, filepath.Join(real, "index.html"), "<h1>hi</h1>", 0o644)
	link := filepath.Join(t.TempDir(), "site")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := MaterializeSource(link, dest); err != nil {
		t.Fatalf("top-level symlink should resolve: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "index.html")); err != nil || string(b) != "<h1>hi</h1>" {
		t.Errorf("index.html wrong: %q err=%v", b, err)
	}
}

func mustWriteFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}
