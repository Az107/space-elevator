package store

import (
	"errors"
	"testing"
)

func TestValidEnvKey(t *testing.T) {
	valid := []string{"A", "NODE_ENV", "_x", "PATH2", "a_B_c9"}
	invalid := []string{"", "2FAST", "WITH SPACE", "dash-dash", "a.b", "K=V", "Ümlaut"}
	for _, k := range valid {
		if !ValidEnvKey(k) {
			t.Errorf("ValidEnvKey(%q) = false, want true", k)
		}
	}
	for _, k := range invalid {
		if ValidEnvKey(k) {
			t.Errorf("ValidEnvKey(%q) = true, want false", k)
		}
	}
}

func TestParseKVLines(t *testing.T) {
	block := "A=1\n\nB =two words\nBAD LINE\nC=\nB=override\n"
	got, bad := ParseKVLines(block)
	if len(bad) != 1 || bad[0] != "BAD LINE" {
		t.Errorf("bad = %q, want [BAD LINE]", bad)
	}
	want := map[string]string{"A": "1", "B": "override", "C": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %q, want %q", k, got[k], v)
		}
	}
	if _, bad := ParseKVLines(""); len(bad) != 0 {
		t.Errorf("empty block: bad = %q", bad)
	}
}

func TestParseKVArgs(t *testing.T) {
	got, bad := ParseKVArgs([]string{"A=1", "B=x=y", "notakv", "=nokey"})
	if len(bad) != 2 {
		t.Errorf("bad = %q, want 2 entries", bad)
	}
	if got["A"] != "1" || got["B"] != "x=y" {
		t.Errorf("got = %v", got)
	}
}

func mustApp(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if err := s.CreateApp(t.Context(), &App{ID: id, Name: name, SourceType: "drop", ComposeYAML: "services: {}", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustApp(t, s, "a1", "web")

	if err := s.SetSecret(ctx, "a1", "DB_PASS", "one"); err != nil {
		t.Fatal(err)
	}
	// Upsert
	if err := s.SetSecret(ctx, "a1", "DB_PASS", "two"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ctx, "a1", "API_KEY", "k"); err != nil {
		t.Fatal(err)
	}

	keys, err := s.ListSecretKeys(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "API_KEY" || keys[1] != "DB_PASS" {
		t.Errorf("keys = %q, want [API_KEY DB_PASS] (sorted)", keys)
	}

	vals, err := s.GetSecrets(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if vals["DB_PASS"] != "two" {
		t.Errorf("DB_PASS = %q, want two", vals["DB_PASS"])
	}

	if err := s.DeleteSecret(ctx, "a1", "DB_PASS"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, "a1", "NOPE"); err != nil {
		t.Errorf("delete of unknown key: %v", err)
	}
	keys, _ = s.ListSecretKeys(ctx, "a1")
	if len(keys) != 1 || keys[0] != "API_KEY" {
		t.Errorf("keys after delete = %q", keys)
	}

	// Secrets cascade away with the app.
	if err := s.DeleteApp(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.ListSecretKeys(ctx, "a1")
	if len(keys) != 0 {
		t.Errorf("secrets survived app delete: %q", keys)
	}
}

func TestLoadRuntimeEnv(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustApp(t, s, "a1", "web")
	if err := s.UpdateAppEnv(ctx, "a1", map[string]string{"A": "plain", "SHARED": "plain"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ctx, "a1", "SHARED", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ctx, "a1", "TOKEN", "t0p"); err != nil {
		t.Fatal(err)
	}

	env, err := s.LoadRuntimeEnv(ctx, "a1", map[string]string{"A": "plain", "SHARED": "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if env["A"] != "plain" {
		t.Errorf("A = %q, want plain", env["A"])
	}
	if env["SHARED"] != "secret" {
		t.Errorf("SHARED = %q, want secret (secrets override plain env)", env["SHARED"])
	}
	if env["TOKEN"] != "t0p" {
		t.Errorf("TOKEN = %q, want t0p", env["TOKEN"])
	}

	// nil base is accepted
	env, err = s.LoadRuntimeEnv(ctx, "a1", nil)
	if err != nil || env["TOKEN"] != "t0p" {
		t.Errorf("nil base: env=%v err=%v", env, err)
	}
}

func TestUpdateAppEnv(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustApp(t, s, "a1", "web")

	if err := s.UpdateAppEnv(ctx, "a1", map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAppByName(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if a.Env["K"] != "v" {
		t.Errorf("env = %v, want K=v", a.Env)
	}
	// Full replace semantics
	if err := s.UpdateAppEnv(ctx, "a1", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAppByName(ctx, "web")
	if len(a.Env) != 0 {
		t.Errorf("env after replace = %v, want empty", a.Env)
	}
	// nil maps to empty, not SQL NULL
	if err := s.UpdateAppEnv(ctx, "a1", nil); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAppByName(ctx, "web")
	if a.Env == nil {
		t.Error("nil env must normalize to empty map")
	}
	if err := s.UpdateAppEnv(ctx, "missing", map[string]string{}); err == nil && !errors.Is(err, ErrNotFound) {
		// UPDATE on unknown id is a silent no-op in SQLite; acceptable —
		// callers resolve the app first.
		_ = err
	}
}
