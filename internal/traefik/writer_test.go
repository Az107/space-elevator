package traefik

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterDisabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	w := NewWriter("", "")
	if err := w.Remove("app"); err != nil {
		t.Fatal(err)
	}
	if err := w.Write("app", AppRouteConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("app.yml"); !os.IsNotExist(err) {
		t.Fatalf("disabled writer touched cwd: %v", err)
	}
}

func TestWriterRejectsReservedDashboardName(t *testing.T) {
	w := NewWriter(t.TempDir(), "")
	if err := w.Write("space-elevator", AppRouteConfig{}); err == nil {
		t.Fatal("expected reserved app name to be rejected")
	}
	if err := w.Remove("space-elevator"); err == nil {
		t.Fatal("expected reserved app removal to be rejected")
	}
}

func TestWriterAtomicWriteAndReadSelf(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, "")
	if err := w.WriteSelf(SelfRouteConfig{Host: "example.test", BackendURL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	body, err := w.ReadSelf()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "example.test") {
		t.Fatalf("self route = %s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, SelfRouteFileName)); err != nil {
		t.Fatal(err)
	}
}
