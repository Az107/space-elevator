package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFunctionBuildValidate(t *testing.T) {
	cases := []struct {
		name    string
		fb      FunctionBuild
		wantErr bool
	}{
		{"python ok", FunctionBuild{Language: "python"}, false},
		{"node ok", FunctionBuild{Language: "node"}, false},
		{"alias js", FunctionBuild{Language: "js", Entrypoint: "index.js:main"}, false},
		{"bad language", FunctionBuild{Language: "ruby"}, true},
		{"empty entrypoint default", FunctionBuild{Language: "python", Entrypoint: ""}, false},
		{"entrypoint traversal", FunctionBuild{Language: "python", Entrypoint: "../evil.py:handler"}, true},
		{"entrypoint absolute", FunctionBuild{Language: "python", Entrypoint: "/etc/passwd:handler"}, true},
		{"entrypoint empty segment", FunctionBuild{Language: "python", Entrypoint: "a//b.py:handler"}, true},
		{"bad symbol", FunctionBuild{Language: "python", Entrypoint: "handler.py:bad-name"}, true},
		{"version injection", FunctionBuild{Language: "python", Version: "3.12; rm -rf /"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fb.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFunctionDockerfile(t *testing.T) {
	fb := FunctionBuild{
		Language:   "python",
		Version:    "3.12",
		Entrypoint: "app/handler.py:run",
		EnvKeys:    []string{"FOO"},
	}
	df := fb.Dockerfile()
	for _, want := range []string{
		"FROM python:3.12-slim",
		"ARG FOO",
		`SE_FUNCTION_ENTRYPOINT="app/handler.py:run"`,
		"requirements.txt",
		"COPY .se-function/server.py /opt/function/server.py",
		`CMD ["python", "/opt/function/server.py"]`,
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile missing %q:\n%s", want, df)
		}
	}
}

func TestWriteFunctionBuild(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFunctionBuild(dir, FunctionBuild{Language: "node", Entrypoint: "index.js:handler"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"Dockerfile", "compose.yml", ".dockerignore", ".se-function/server.mjs"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if !strings.Contains(string(compose), `"8080"`) {
		t.Errorf("compose should publish port 8080:\n%s", compose)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if !strings.Contains(string(df), "FROM node:20-slim") {
		t.Errorf("node Dockerfile wrong base:\n%s", df)
	}
}

func TestParseEntrypoint(t *testing.T) {
	file, symbol := ParseEntrypoint("python", "")
	if file != "handler.py" || symbol != "handler" {
		t.Errorf("default = %q:%q", file, symbol)
	}
	file, symbol = ParseEntrypoint("node", "src/app.js")
	if file != "src/app.js" || symbol != "handler" {
		t.Errorf("no-symbol = %q:%q", file, symbol)
	}
	file, symbol = ParseEntrypoint("node", "src/app.js:main")
	if file != "src/app.js" || symbol != "main" {
		t.Errorf("explicit = %q:%q", file, symbol)
	}
}

func TestFunctionDefaults(t *testing.T) {
	if DefaultVersion("node") != "20" || DefaultVersion("python") != "3.12" {
		t.Error("unexpected default versions")
	}
	if DefaultEntrypoint("node") != "index.js:handler" {
		t.Error("unexpected node entrypoint default")
	}
}
