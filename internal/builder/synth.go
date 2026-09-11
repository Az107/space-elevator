package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CustomBuild describes an "advanced deploy": a repo that isn't
// containerized, deployed by synthesizing a single-stage Dockerfile
// from a builder image plus build/run commands. The synthesized files
// feed the exact same compose→build→run pipeline as drops, so nothing
// downstream (labels, Traefik, limits) changes.
type CustomBuild struct {
	// BuilderImage is the base image the app builds and runs in, e.g.
	// "node:20-bookworm" or "golang:1.22". Required.
	BuilderImage string
	// BuildCommand runs inside the image after COPY (e.g. "npm ci &&
	// npm run build"). Optional — interpreted programs that run from
	// source don't need one.
	BuildCommand string
	// RunCommand is the container's CMD (e.g. "npm start" or
	// "./bin/server"). Required. Always executed via /bin/sh -c.
	RunCommand string
	// ListenPort is the port the app serves on; published to a random
	// host port and used by Traefik routing. Zero means 8080.
	ListenPort int
	// EnvKeys lists the app's plain env var names. Each is declared as
	// a Dockerfile ARG so the values passed with --build-arg (the
	// runtime does this for plain env) are visible to the build
	// command — e.g. Vite/Next inlining env at build time. Secrets are
	// never included.
	EnvKeys []string
}

// DefaultListenPort is used when CustomBuild.ListenPort is unset.
const DefaultListenPort = 8080

// Port resolves the effective listen port.
func (c CustomBuild) Port() int {
	if c.ListenPort <= 0 {
		return DefaultListenPort
	}
	return c.ListenPort
}

// Validate checks the fields the synthesizer can't invent.
func (c CustomBuild) Validate() error {
	if strings.TrimSpace(c.BuilderImage) == "" {
		return fmt.Errorf("builder image required (e.g. node:20-bookworm)")
	}
	if strings.ContainsAny(c.BuilderImage, " \t\n;$&|<>`") {
		return fmt.Errorf("builder image %q contains unexpected characters", c.BuilderImage)
	}
	if strings.TrimSpace(c.RunCommand) == "" {
		return fmt.Errorf("run command required (the container's CMD)")
	}
	if p := c.Port(); p < 1 || p > 65535 {
		return fmt.Errorf("listen port %d out of range (1-65535)", p)
	}
	return nil
}

// Dockerfile renders the single-stage Dockerfile.
func (c CustomBuild) Dockerfile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", c.BuilderImage)
	for _, k := range c.EnvKeys {
		fmt.Fprintf(&b, "ARG %s\n", k)
	}
	b.WriteString("WORKDIR /app\n")
	b.WriteString("COPY . .\n")
	if cmd := strings.TrimSpace(c.BuildCommand); cmd != "" {
		// Shell form: the command is a user-supplied shell snippet.
		b.WriteString("RUN " + cmd + "\n")
	}
	fmt.Fprintf(&b, "EXPOSE %d\n", c.Port())
	// JSON form via json.Marshal guarantees correct quoting of the
	// user-supplied command inside /bin/sh -c.
	shellCmd, _ := json.Marshal([]string{"/bin/sh", "-c", c.RunCommand})
	b.WriteString("CMD " + string(shellCmd) + "\n")
	return b.String()
}

// Compose renders the synthetic single-service compose file. Service
// name "web" matches the static-drop convention so Traefik path-prefix
// routes stay uniform. The bare port spec publishes a random host port,
// which the Traefik resolver picks up via InspectIPs.
func (c CustomBuild) Compose() string {
	return fmt.Sprintf(`services:
  web:
    build: .
    ports:
      - "%d"
`, c.Port())
}

// WriteCustomBuild writes the synthesized Dockerfile, compose.yml, and
// a .dockerignore (exclude .git) into destDir. It overwrites any
// previous synth output so redeploys pick up edited build settings.
func WriteCustomBuild(destDir string, c CustomBuild) error {
	if err := c.Validate(); err != nil {
		return err
	}
	files := map[string]string{
		"Dockerfile":   c.Dockerfile(),
		"compose.yml":  c.Compose(),
		".dockerignore": ".git\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(destDir, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// ParsePort parses a user-supplied port string; empty means default.
func ParsePort(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultListenPort, nil
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q (use 1-65535)", s)
	}
	return p, nil
}
