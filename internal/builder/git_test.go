package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ref  string
	}{
		{name: "github root", in: "https://github.com/user/repo", want: "https://github.com/user/repo.git"},
		{name: "github tree", in: "https://github.com/user/repo/tree/master/docs", want: "https://github.com/user/repo.git", ref: "master"},
		{name: "github blob", in: "https://github.com/user/repo/blob/main/README.md", want: "https://github.com/user/repo.git", ref: "main"},
		{name: "gitlab tree", in: "https://gitlab.com/group/project/-/tree/release", want: "https://gitlab.com/group/project.git", ref: "release"},
		{name: "credentials stripped", in: "https://user:password@github.com/user/repo", want: "https://github.com/user/repo.git"},
		{name: "ssh unchanged", in: "git@github.com:user/repo.git", want: "git@github.com:user/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeGitURL(tt.in); got != tt.want {
				t.Errorf("NormalizeGitURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			gotRef, ok := RefFromWebURL(tt.in)
			if ok != (tt.ref != "") || (ok && gotRef != tt.ref) {
				t.Errorf("RefFromWebURL(%q) = %q, %v; want %q, %v", tt.in, gotRef, ok, tt.ref, tt.ref != "")
			}
		})
	}
}

func TestFriendlyCloneErrorDoesNotExposeHTML(t *testing.T) {
	err := friendlyCloneError(fmt.Errorf("repository not found: <!DOCTYPE html><html><body>large response</body></html>"))
	if strings.Contains(err.Error(), "<!DOCTYPE") || len(err.Error()) > 200 {
		t.Fatalf("error was not sanitized: %v", err)
	}
	if !strings.Contains(err.Error(), "not found or inaccessible") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloneReferenceName(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want plumbing.ReferenceName
		err  bool
	}{
		{name: "short branch", ref: "main", want: ""},
		{name: "nested branch", ref: "release/next", want: ""},
		{name: "full branch", ref: "refs/heads/main", want: plumbing.NewBranchReferenceName("main")},
		{name: "full tag", ref: "refs/tags/v1.2.3", want: plumbing.NewTagReferenceName("v1.2.3")},
		{name: "short tag needs all refs", ref: "v1.2.3", want: ""},
		{name: "commit", ref: "0123456789abcdef", want: ""},
		{name: "head", ref: "HEAD", want: ""},
		{name: "empty", ref: "", err: true},
		{name: "invalid", ref: "refs/heads/bad name", err: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneReferenceName(tt.ref)
			if tt.err {
				if err == nil {
					t.Fatalf("cloneReferenceName(%q) succeeded, want error", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("cloneReferenceName(%q): %v", tt.ref, err)
			}
			if got != tt.want {
				t.Errorf("cloneReferenceName(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveRefSupportsBranchesTagsAndCommits(t *testing.T) {
	repo, commit := newTestGitRepository(t)
	signature := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0)}
	if _, err := repo.CreateTag("release", commit, &git.CreateTagOptions{
		Message: "release",
		Tagger:  &signature,
	}); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	branch := head.Name().Short()
	refs := []string{
		branch,
		head.Name().String(),
		"release",
		"refs/tags/release",
		commit.String(),
		commit.String()[:12],
		"HEAD",
	}
	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			got, err := resolveRef(repo, ref)
			if err != nil {
				t.Fatalf("resolveRef(%q): %v", ref, err)
			}
			if *got != commit {
				t.Errorf("resolveRef(%q) = %s, want %s", ref, got, commit)
			}
		})
	}

	if _, err := resolveRef(repo, "does-not-exist"); err == nil {
		t.Error("resolveRef for a missing ref unexpectedly succeeded")
	}
}

func TestCloneChecksOutBranchesTagsAndCommits(t *testing.T) {
	sourceDir := t.TempDir()
	repo, firstCommit := newTestGitRepositoryAt(t, sourceDir)
	signature := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0)}
	if _, err := repo.CreateTag("release", firstCommit, &git.CreateTagOptions{
		Message: "release",
		Tagger:  &signature,
	}); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "README"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("update file: %v", err)
	}
	if _, err := worktree.Add("README"); err != nil {
		t.Fatalf("stage update: %v", err)
	}
	secondCommit, err := worktree.Commit("second commit", &git.CommitOptions{Author: &signature})
	if err != nil {
		t.Fatalf("commit update: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("feature"), secondCommit)); err != nil {
		t.Fatalf("create feature branch: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("read source HEAD: %v", err)
	}

	tests := []struct {
		name string
		ref  string
		want plumbing.Hash
		url  string
	}{
		{name: "branch", ref: head.Name().String(), want: secondCommit},
		{name: "short non-default branch", ref: "feature", want: secondCommit},
		{name: "tag", ref: "release", want: firstCommit},
		{name: "commit", ref: firstCommit.String(), want: firstCommit},
		{name: "short commit", ref: firstCommit.String()[:12], want: firstCommit},
		{name: "short commit file url", ref: firstCommit.String()[:12], want: firstCommit, url: "file://" + sourceDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			destDir := filepath.Join(t.TempDir(), "checkout")
			url := tt.url
			if url == "" {
				url = sourceDir
			}
			if err := Clone(t.Context(), url, tt.ref, destDir, nil); err != nil {
				t.Fatalf("clone %q: %v", tt.ref, err)
			}
			checkout, err := git.PlainOpen(destDir)
			if err != nil {
				t.Fatalf("open checkout: %v", err)
			}
			checkoutHead, err := checkout.Head()
			if err != nil {
				t.Fatalf("read checkout HEAD: %v", err)
			}
			if checkoutHead.Hash() != tt.want {
				t.Errorf("checkout HEAD = %s, want %s", checkoutHead.Hash(), tt.want)
			}
		})
	}
}

func newTestGitRepository(t *testing.T) (*git.Repository, plumbing.Hash) {
	t.Helper()
	return newTestGitRepositoryAt(t, t.TempDir())
}

func newTestGitRepositoryAt(t *testing.T, dir string) (*git.Repository, plumbing.Hash) {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repository: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := worktree.Add("README"); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	commit, err := worktree.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0)},
	})
	if err != nil {
		t.Fatalf("commit file: %v", err)
	}
	return repo, commit
}
