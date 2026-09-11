package store

import (
	"errors"
	"testing"
	"time"
)

func TestMintAndGetAPIToken(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	tok, raw, err := s.MintAPIToken(ctx, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "ci" || tok.Prefix == "" {
		t.Errorf("unexpected token %+v", tok)
	}
	if len(raw) < 40 {
		t.Errorf("raw token too short: %q", raw)
	}

	got, err := s.GetAPIToken(ctx, raw)
	if err != nil {
		t.Fatalf("raw lookup failed: %v", err)
	}
	if got.ID != tok.ID || got.Name != "ci" {
		t.Errorf("got %+v, want id %s", got, tok.ID)
	}
	// The hash must not validate as a token.
	if _, err := s.GetAPIToken(ctx, tok.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Error("hash must not be accepted as a raw token")
	}
	// Unknown / wrong prefix tokens must not resolve.
	if _, err := s.GetAPIToken(ctx, "se_wrong"); !errors.Is(err, ErrNotFound) {
		t.Error("unknown token must not resolve")
	}
	if _, err := s.GetAPIToken(ctx, "not-a-token"); !errors.Is(err, ErrNotFound) {
		t.Error("token without prefix must not resolve")
	}
}

func TestAPITokenExpiry(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	past := time.Now().Add(-time.Hour)
	_, raw, err := s.MintAPIToken(ctx, "expired", &past)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPIToken(ctx, raw); !errors.Is(err, ErrNotFound) {
		t.Error("expired token must not resolve")
	}

	future := time.Now().Add(time.Hour)
	_, raw2, err := s.MintAPIToken(ctx, "live", &future)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPIToken(ctx, raw2); err != nil {
		t.Errorf("unexpired token must resolve: %v", err)
	}
}

func TestListTouchDeleteAPIToken(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	if _, _, err := s.MintAPIToken(ctx, "b", nil); err != nil {
		t.Fatal(err)
	}
	tokA, _, err := s.MintAPIToken(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}

	toks, err := s.ListAPITokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 2 {
		t.Fatalf("tokens = %d, want 2", len(toks))
	}
	// Descending by creation; both created within the same second so
	// order may tie — just check both names are present.
	names := map[string]bool{}
	for _, tk := range toks {
		names[tk.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("missing tokens: %v", names)
	}

	at := time.Now()
	if err := s.TouchAPIToken(ctx, tokA.ID, at); err != nil {
		t.Fatal(err)
	}
	toks, _ = s.ListAPITokens(ctx)
	for _, tk := range toks {
		if tk.ID == tokA.ID && tk.LastUsedAt == nil {
			t.Error("last_used_at not recorded")
		}
	}

	if err := s.DeleteAPIToken(ctx, tokA.ID); err != nil {
		t.Fatal(err)
	}
	toks, _ = s.ListAPITokens(ctx)
	if len(toks) != 1 {
		t.Errorf("tokens after delete = %d, want 1", len(toks))
	}
}

func TestMintAPITokenRequiresName(t *testing.T) {
	s := openTestStore(t)
	if _, _, err := s.MintAPIToken(t.Context(), "  ", nil); err == nil {
		t.Error("empty name must be rejected")
	}
}
