package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsAreNeutral(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	cfg := MustLoad("")
	if cfg.PublicHost != "" {
		t.Errorf("PublicHost = %q, want empty", cfg.PublicHost)
	}
	if cfg.TraefikDir != "" {
		t.Errorf("TraefikDir = %q, want empty", cfg.TraefikDir)
	}
	if cfg.CertResolver != "" {
		t.Errorf("CertResolver = %q, want empty", cfg.CertResolver)
	}
	if cfg.BindAddr != "127.0.0.1:8080" {
		t.Errorf("BindAddr = %q", cfg.BindAddr)
	}
}

func TestLoadFileThenEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "public_host: file.example.com\ncert_resolver: file-resolver\nbind_addr: 1.2.3.4:9000\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPACE_ELEVATOR_PUBLIC_HOST", "env.example.com")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicHost != "env.example.com" {
		t.Errorf("env should win: PublicHost = %q", cfg.PublicHost)
	}
	if cfg.CertResolver != "file-resolver" {
		t.Errorf("file value lost: CertResolver = %q", cfg.CertResolver)
	}
	if cfg.BindAddr != "1.2.3.4:9000" {
		t.Errorf("file bind_addr lost: %q", cfg.BindAddr)
	}
}

func TestFileEmptyDisablesIntegration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Explicit empty values must override non-empty defaults, not be ignored.
	body := "public_host: \"\"\ntraefik_dir: \"\"\ncert_resolver: \"\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicHost != "" || cfg.TraefikDir != "" || cfg.CertResolver != "" {
		t.Errorf("explicit empties not applied: %+v", cfg)
	}
}

func TestDeriveDashboardURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Direct mode -> loopback backend.
	if err := os.WriteFile(path, []byte("bind_addr: 127.0.0.1:8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(path)
	if cfg.DashboardURL != "http://127.0.0.1:8080" {
		t.Errorf("DashboardURL = %q", cfg.DashboardURL)
	}

	// Traefik enabled -> host.containers.internal backend.
	if err := os.WriteFile(path, []byte("bind_addr: 0.0.0.0:8080\ntraefik_dir: /tmp/traefik\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(path)
	if cfg.DashboardURL != "http://host.containers.internal:8080" {
		t.Errorf("DashboardURL = %q", cfg.DashboardURL)
	}
}

func TestValidateWarnings(t *testing.T) {
	cfg := BuiltinDefaults()
	cfg.PublicHost = "demo.example.com"
	cfg.TraefikDir = "/tmp/t"
	cfg.CertResolver = ""
	issues := cfg.Validate()
	found := false
	for _, i := range issues {
		if i.Level == "warning" && strings.Contains(i.Message, "cert_resolver") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cert_resolver warning, got %+v", issues)
	}

	cfg.BindAddr = "not-a-host-port"
	if len(cfg.Errors()) == 0 {
		t.Error("expected an error for a malformed bind_addr")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.yaml")
	cfg := BuiltinDefaults()
	cfg.PublicHost = "elevator.example.com"
	cfg.TraefikDir = "/srv/traefik/dynamic"
	cfg.CertResolver = "letsencrypt"
	cfg.RootlessGateway = "10.89.0.1"

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "elevator.example.com") {
		t.Errorf("saved file missing public host:\n%s", b)
	}
	if !strings.Contains(string(b), "# env: SPACE_ELEVATOR_PUBLIC_HOST") {
		t.Errorf("saved file missing documentation comments:\n%s", b)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if got.PublicHost != cfg.PublicHost || got.TraefikDir != cfg.TraefikDir ||
		got.CertResolver != cfg.CertResolver || got.RootlessGateway != cfg.RootlessGateway {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestSaveRefusesSymlinkAndValidatesRouteParts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := (BuiltinDefaults()).Save(path); err == nil {
		t.Fatal("Save followed a config symlink")
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "keep" {
		t.Fatalf("symlink target changed: %q", got)
	}
	cfg := BuiltinDefaults()
	cfg.PublicHost = "https://bad.example/"
	if len(cfg.Errors()) == 0 {
		t.Fatal("invalid public host was accepted")
	}
}

func TestTokenManagerConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "token_manager_url: http://manager.example\n" +
		"token_manager_client_id: app_space\n" +
		"token_manager_client_secret: cs_secret\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TokenManagerConfigured() || cfg.TokenManagerClientSecret != "cs_secret" {
		t.Fatalf("Token-Manager config = %+v", cfg)
	}

	cfg.TokenManagerClientSecret = ""
	if !cfg.TokenManagerPartiallyConfigured() {
		t.Fatal("partial Token-Manager config was not detected")
	}
	if len(cfg.Errors()) == 0 {
		t.Fatal("partial Token-Manager config should be an error")
	}
}

func TestTokenManagerEnvPrecedence(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("SPACE_ELEVATOR_TOKEN_MANAGER_URL", "http://env-manager")
	t.Setenv("SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_ID", "env-id")
	t.Setenv("SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_SECRET", "env-secret")
	cfg := MustLoad("")
	if cfg.TokenManagerURL != "http://env-manager" || cfg.TokenManagerClientID != "env-id" || cfg.TokenManagerClientSecret != "env-secret" {
		t.Fatalf("env Token-Manager config = %+v", cfg)
	}
}

func TestUpdateRepoDefaultAndEnv(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	if got := MustLoad("").UpdateRepo; got == "" {
		t.Fatal("update_repo default is empty; the updater would be disabled")
	}
	t.Setenv("SPACE_ELEVATOR_UPDATE_REPO", "https://github.com/other/repo")
	if got := MustLoad("").UpdateRepo; got != "https://github.com/other/repo" {
		t.Fatalf("UpdateRepo env override = %q", got)
	}
}

func TestUpdateRepoIsOnlyAWarning(t *testing.T) {
	cfg := BuiltinDefaults()
	cfg.UpdateRepo = "not-a-repo"
	if errs := cfg.Errors(); len(errs) != 0 {
		t.Fatalf("a bad update_repo must not be error-level: %v", errs)
	}
	found := false
	for _, i := range cfg.Validate() {
		if i.Level == "warning" && strings.Contains(i.Message, "update_repo") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an update_repo warning")
	}
}

func TestDetectLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Infra", "traefik", "rootful-dynamic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := "http:\n  routers:\n    space-elevator-secure:\n" +
		"      rule: Host(`legacy.example.com`)\n" +
		"      tls:\n        certResolver: myresolver\n"
	if err := os.WriteFile(filepath.Join(dir, "space-elevator.yml"), []byte(self), 0o644); err != nil {
		t.Fatal(err)
	}
	app := "http:\n  services:\n    demo-web:\n      loadBalancer:\n" +
		"        servers:\n          - url: http://10.89.0.1:34909\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.yml"), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}

	l := DetectLegacy()
	if !l.Found {
		t.Fatal("expected legacy setup to be detected")
	}
	if l.PublicHost != "legacy.example.com" {
		t.Errorf("PublicHost = %q", l.PublicHost)
	}
	if l.CertResolver != "myresolver" {
		t.Errorf("CertResolver = %q", l.CertResolver)
	}
	if l.RootlessGateway != "10.89.0.1" {
		t.Errorf("RootlessGateway = %q", l.RootlessGateway)
	}
	if l.BindAddr != "0.0.0.0:8080" {
		t.Errorf("BindAddr = %q", l.BindAddr)
	}
}

func TestFormatSizeBytes(t *testing.T) {
	cases := map[int64]string{
		0:         "0",
		512 << 20: "512M",
		1 << 30:   "1G",
		2 * 1024:  "2K",
		1500:      "1500",
	}
	for in, want := range cases {
		if got := FormatSizeBytes(in); got != want {
			t.Errorf("FormatSizeBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
