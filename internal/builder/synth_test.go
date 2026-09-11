package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validBuild() CustomBuild {
	return CustomBuild{
		BuilderImage: "node:20-bookworm",
		BuildCommand: "npm ci && npm run build",
		RunCommand:   `node dist/server.js --port "$PORT"`,
		ListenPort:   3000,
	}
}

func TestCustomBuildValidate(t *testing.T) {
	if err := validBuild().Validate(); err != nil {
		t.Fatalf("valid build rejected: %v", err)
	}
	cases := map[string]CustomBuild{
		"no image":     {BuilderImage: "", RunCommand: "x", ListenPort: 80},
		"no run":       {BuilderImage: "node:20", RunCommand: " ", ListenPort: 80},
		"image spaces": {BuilderImage: "node:20 && rm -rf /", RunCommand: "x", ListenPort: 80},
		"port low":     {BuilderImage: "node:20", RunCommand: "x", ListenPort: 0, // handled by Port(), raw 0 invalid for range? see below
		},
	}
	// Port 0 is allowed at validate (resolves to default); test explicit bad ports instead.
	badPort := validBuild()
	badPort.ListenPort = 70000
	cases["port high"] = badPort
	for name, c := range cases {
		if name == "port low" {
			if err := c.Validate(); err != nil {
				t.Errorf("%s: port 0 should resolve to default, got %v", name, err)
			}
			continue
		}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCustomBuildPortDefault(t *testing.T) {
	c := validBuild()
	c.ListenPort = 0
	if got := c.Port(); got != DefaultListenPort {
		t.Errorf("Port() = %d, want %d", got, DefaultListenPort)
	}
}

func TestCustomBuildDockerfile(t *testing.T) {
	d := validBuild().Dockerfile()
	for _, want := range []string{
		"FROM node:20-bookworm",
		"WORKDIR /app",
		"COPY . .",
		"RUN npm ci && npm run build",
		"EXPOSE 3000",
		`CMD ["/bin/sh","-c","node dist/server.js --port \"$PORT\""]`,
	} {
		if !strings.Contains(d, want) {
			t.Errorf("dockerfile missing %q:\n%s", want, d)
		}
	}
}

func TestCustomBuildDockerfileNoBuildCmd(t *testing.T) {
	c := validBuild()
	c.BuildCommand = ""
	if strings.Contains(c.Dockerfile(), "RUN ") {
		t.Error("empty build command must not emit a RUN line")
	}
}

func TestCustomBuildCompose(t *testing.T) {
	got := validBuild().Compose()
	want := "services:\n  web:\n    build: .\n    ports:\n      - \"3000\"\n"
	if got != want {
		t.Errorf("compose = %q, want %q", got, want)
	}
}

func TestWriteCustomBuild(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCustomBuild(dir, validBuild()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Dockerfile", "compose.yml", ".dockerignore"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	di, _ := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if string(di) != ".git\n" {
		t.Errorf(".dockerignore = %q", di)
	}
	if err := WriteCustomBuild(dir, CustomBuild{RunCommand: "x"}); err == nil {
		t.Error("invalid build must not write files")
	}
}

func TestParsePort(t *testing.T) {
	if p, err := ParsePort(""); err != nil || p != DefaultListenPort {
		t.Errorf("empty: %d %v", p, err)
	}
	if p, err := ParsePort("3000"); err != nil || p != 3000 {
		t.Errorf("3000: %d %v", p, err)
	}
	for _, bad := range []string{"0", "70000", "abc", "-1"} {
		if _, err := ParsePort(bad); err == nil {
			t.Errorf("ParsePort(%q) should fail", bad)
		}
	}
}

func TestCustomBuildDockerfileEnvArgs(t *testing.T) {
	c := validBuild()
	c.EnvKeys = []string{"VITE_SUPABASE_URL", "VITE_API_KEY"}
	d := c.Dockerfile()
	if !strings.Contains(d, "ARG VITE_SUPABASE_URL\n") || !strings.Contains(d, "ARG VITE_API_KEY\n") {
		t.Errorf("dockerfile missing ARG lines for env keys:\n%s", d)
	}
	// ARGs must be declared before the RUN that consumes them.
	if strings.Index(d, "ARG VITE_SUPABASE_URL") > strings.Index(d, "RUN npm ci") {
		t.Errorf("ARG lines must precede the build RUN:\n%s", d)
	}
	// No keys, no ARG lines (default rendering unchanged).
	if strings.Contains(validBuild().Dockerfile(), "ARG ") {
		t.Error("empty EnvKeys must not emit ARG lines")
	}
}
