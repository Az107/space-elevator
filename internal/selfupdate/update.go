package selfupdate

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// SmokeTest runs `<candidate> --version` and requires exit 0 with output.
// This mirrors the deploy script's gate: a binary that cannot even print its
// version is never installed. `--version` must not touch state, so this is
// safe to run while the current binary is still serving.
func SmokeTest(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return fmt.Errorf("candidate failed `--version`: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("candidate `--version` printed nothing")
	}
	return nil
}

// ProbeURL turns a bind address into a loopback URL for the health probe.
func ProbeURL(bindAddr string) string {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil || port == "" {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// Healthy GETs the dashboard root and reports whether it answered below 400.
// This is the production gate (`curl -sf`); the unauthenticated root
// redirects to /login (303), which counts as healthy.
func Healthy(ctx context.Context, url string) bool {
	if url == "" {
		return true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}
