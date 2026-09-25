package builder

import (
	"errors"
	"testing"
)

// TestValidateRemoteURLIsPolicyNotNormalizer documents the split: git's Clone
// accepts file:// and local paths (the unit tests rely on that), but a deploy
// must not, or anyone who can create an app could read and build arbitrary
// directories on the host.
func TestValidateRemoteURLIsPolicyNotNormalizer(t *testing.T) {
	allowed := []string{
		"https://github.com/org/repo",
		"https://github.com/org/repo.git",
		"http://git.internal/team/repo.git",
		"https://github.com/org/repo/tree/main",
		"HTTPS://github.com/org/repo",
	}
	for _, in := range allowed {
		t.Run("allow/"+in, func(t *testing.T) {
			if err := ValidateRemoteURL(in); err != nil {
				t.Fatalf("http(s) URL rejected: %v", err)
			}
		})
	}

	// Each of these is clonable by go-git but must not be deployable.
	denied := []struct {
		in  string
		why string
	}{
		{in: "file:///home/me/.ssh", why: "local file transport reads the host filesystem"},
		{in: "file:///etc", why: "local file transport"},
		{in: "/home/me/repo", why: "bare local path"},
		{in: "../relative/repo", why: "relative local path"},
		{in: "git@github.com:org/repo.git", why: "ssh has no key management in the platform"},
		{in: "ssh://git@github.com/org/repo.git", why: "ssh has no key management"},
		{in: "git://github.com/org/repo.git", why: "unauthenticated git protocol"},
		{in: "", why: "empty"},
		{in: "   ", why: "blank"},
		{in: "https://", why: "no host"},
	}
	for _, tc := range denied {
		t.Run("deny/"+tc.why, func(t *testing.T) {
			err := ValidateRemoteURL(tc.in)
			if err == nil {
				t.Fatalf("ValidateRemoteURL(%q) accepted it: %s", tc.in, tc.why)
			}
			if !errors.Is(err, ErrUnsupportedGitURL) {
				t.Fatalf("error should wrap ErrUnsupportedGitURL, got %v", err)
			}
		})
	}
}

// TestValidateRemoteURLRejectsControlChars keeps log/header injection out of
// the persisted source_ref and the clone URL. Surrounding whitespace is
// tolerated (and normalised away, matching NormalizeGitURL, so a URL pasted
// from a file with a trailing newline still works); an *interior* control
// character is a malformed input and is rejected.
func TestValidateRemoteURLRejectsControlChars(t *testing.T) {
	for _, in := range []string{
		"https://github.com/org/\x00repo",
		"https://github.com/org/re\npo",
		"https://github.com/org/repo\x7f",
	} {
		if err := ValidateRemoteURL(in); err == nil {
			t.Fatalf("interior control character accepted in %q", in)
		}
	}

	// Surrounding whitespace is trimmed, not rejected.
	if err := ValidateRemoteURL("  https://github.com/org/repo\n"); err != nil {
		t.Fatalf("surrounding whitespace should be tolerated: %v", err)
	}
}
