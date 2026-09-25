package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSizeBytesFailsClosed is the regression guard for a silent
// fail-open: parseSizeBytes used to return 0 on any parse error, and 0 means
// "unlimited" to the container runtime. A typo therefore removed the memory
// cap instead of being reported.
func TestParseSizeBytesFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "512M", want: 512 * 1024 * 1024},
		{in: "1G", want: 1024 * 1024 * 1024},
		{in: "2048", want: 2048},
		{in: "0", want: 0},
		{in: "", want: 0},
		{in: "512Mi", wantErr: true},
		{in: "512mb", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "1T", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSizeBytes(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSizeBytes(%q) = %d, want an error (0 would mean unlimited)", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSizeBytes(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseSizeBytes(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseInt64FailsClosed(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "256", want: 256},
		{in: "0", want: 0},
		{in: "", want: 0},
		{in: "none", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "-5", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseInt64(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseInt64(%q) = %d, want an error (0 would mean no limit)", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInt64(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseInt64(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestInsecureCookiesEnvIsStrict guards the security-relevant default:
// SPACE_ELEVATOR_INSECURE_COOKIES=false used to evaluate to *true* under
// `v != ""`, stripping Secure from the session cookie on a well-meaning
// operator edit.
func TestInsecureCookiesEnvIsStrict(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{env: "false", want: false},
		{env: "0", want: false},
		{env: "F", want: false},
		{env: "true", want: true},
		{env: "1", want: true},
		{env: "TRUE", want: true},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SPACE_ELEVATOR_INSECURE_COOKIES", tc.env)
			t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.InsecureCookies != tc.want {
				t.Fatalf("InsecureCookies for %q = %v, want %v", tc.env, cfg.InsecureCookies, tc.want)
			}
		})
	}
}

// TestInsecureCookiesEnvRejectsNonBooleanLiterals pins the strict contract:
// unlike the old `v != ""` check, a word Go's ParseBool does not recognise
// must fail the load rather than resolving to either value. "no" in
// particular used to mean "true" (insecure) when the operator meant false.
func TestInsecureCookiesEnvRejectsNonBooleanLiterals(t *testing.T) {
	for _, env := range []string{"no", "yes", "on", "off", "enabled", "yes-please"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("SPACE_ELEVATOR_INSECURE_COOKIES", env)
			t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
			if _, err := Load(""); err == nil {
				t.Fatalf("non-boolean value %q was accepted", env)
			}
		})
	}
}

func TestInsecureCookiesEnvRejectsGarbage(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_INSECURE_COOKIES", "yes-please")
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("non-boolean INSECURE_COOKIES value was accepted")
	}
}

// TestMemoryLimitEnvRejectsGarbage makes sure an unparseable limit fails the
// load instead of silently disabling the cap.
func TestMemoryLimitEnvRejectsGarbage(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_MEMORY_LIMIT", "512Mi")
	t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	_, err := Load("")
	if err == nil {
		t.Fatal("unparseable memory limit was accepted as unlimited")
	}
	if !strings.Contains(err.Error(), "MEMORY_LIMIT") {
		t.Fatalf("error should name the variable, got: %v", err)
	}
}

func TestPidsLimitEnvRejectsGarbage(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_PIDS_LIMIT", "none")
	t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	_, err := Load("")
	if err == nil {
		t.Fatal("unparseable pids limit was accepted as unlimited")
	}
}

func TestYAMLMemoryLimitRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("memory_limit: 512Mi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unparseable memory_limit in YAML was accepted as unlimited")
	}
}
