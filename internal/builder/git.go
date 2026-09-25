package builder

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

type Auth struct {
	Username string
	Token    string
}

// NormalizeGitURL accepts the repository URLs people commonly copy from a
// browser as well as actual Git clone URLs. GitHub's /tree/<branch> and
// GitLab's /-/tree/<branch> URLs are web pages, not Git transports; go-git
// otherwise appends /info/refs to the page path and receives a large HTML 404
// document instead of a useful clone error.
func NormalizeGitURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\x00\r\n") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return stripMalformedURLCredentials(raw)
	}
	u.RawQuery = ""
	u.Fragment = ""
	// Credentials in a repository URL would be persisted in the app row and
	// emitted in logs/audit records. The supported credential path is the
	// per-host credential store, so never carry URL userinfo forward.
	u.User = nil
	u.Path = strings.TrimRight(u.Path, "/")
	for _, marker := range []string{"/-/tree/", "/-/blob/", "/-/commits/", "/-/releases/", "/tree/", "/blob/", "/commits/", "/releases/"} {
		if index := strings.Index(u.Path, marker); index >= 0 {
			u.Path = u.Path[:index]
			break
		}
	}
	if u.Path != "" && !strings.HasSuffix(strings.ToLower(u.Path), ".git") {
		u.Path += ".git"
	}
	return u.String()
}

// ErrUnsupportedGitURL marks a repository URL the platform will not clone.
var ErrUnsupportedGitURL = errors.New("unsupported git URL")

// ValidateRemoteURL enforces the platform's *policy* on what may be cloned,
// which is deliberately narrower than what git.PlainCloneContext accepts.
//
// NormalizeGitURL is a normaliser: it rewrites web URLs and leaves anything
// else alone, so it still round-trips scp-style and file:// inputs. That is
// fine for the Clone helper, but a deploy must not accept them — `file://`
// and bare local paths let anyone who can create an app read and build
// arbitrary directories on the host, and the git transport follows the
// scheme. Only http(s) is accepted until SSH has real key management.
func ValidateRemoteURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%w: URL is empty", ErrUnsupportedGitURL)
	}
	if strings.ContainsAny(trimmed, "\x00\r\n") {
		return fmt.Errorf("%w: URL contains control characters", ErrUnsupportedGitURL)
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedGitURL, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return fmt.Errorf("%w: URL has no host", ErrUnsupportedGitURL)
		}
		return nil
	case "":
		return fmt.Errorf("%w: %q is a local path; use an http(s) repository URL", ErrUnsupportedGitURL, trimmed)
	default:
		return fmt.Errorf("%w: scheme %q is not allowed; use http or https", ErrUnsupportedGitURL, u.Scheme)
	}
}

func stripMalformedURLCredentials(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return raw
	}
	rest := raw[schemeEnd+3:]
	slash := strings.IndexAny(rest, "/?#")
	authority := rest
	if slash >= 0 {
		authority = rest[:slash]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw
	}
	return raw[:schemeEnd+3] + authority[at+1:] + rest[len(authority):]
}

// RefFromWebURL returns the branch embedded in common repository browser
// URLs, such as https://github.com/user/repo/tree/main. It deliberately
// returns only the first branch segment; callers can still override it with
// an explicit ref for branches containing slashes.
func RefFromWebURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	path := strings.TrimRight(u.Path, "/")
	for _, marker := range []string{"/-/tree/", "/-/blob/", "/-/commits/", "/-/releases/", "/tree/", "/blob/", "/commits/", "/releases/"} {
		if index := strings.Index(path, marker); index >= 0 {
			branch := strings.Trim(path[index+len(marker):], "/")
			if branch == "" {
				return "", false
			}
			if slash := strings.IndexByte(branch, '/'); slash >= 0 {
				branch = branch[:slash]
			}
			return branch, true
		}
	}
	return "", false
}

func friendlyCloneError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	if strings.Contains(lower, "repository not found") {
		return fmt.Errorf("repository not found or inaccessible; check the repository URL, ref, and credentials")
	}
	if strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html") {
		return fmt.Errorf("git server returned an HTML page instead of a Git response; use the repository clone URL and verify the ref")
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return fmt.Errorf("git clone failed: %s", message)
}

// Clone clones a git repo into destDir and checks out the given branch, tag,
// or commit. Branch and tag names may be short or fully qualified.
func Clone(ctx context.Context, repoURL, ref, destDir string, auth *Auth) error {
	ref = strings.TrimSpace(ref)
	repoURL = NormalizeGitURL(repoURL)
	referenceName, err := cloneReferenceName(ref)
	if err != nil {
		return err
	}

	authMethod := gitAuth(auth, repoURL)
	depth := 1
	if looksLikeCommitSHA(ref) {
		// A commit may be an ancestor of the default branch, so a shallow
		// clone would not contain the requested object.
		depth = 0
	}
	cloneOptions := &git.CloneOptions{
		URL:           repoURL,
		ReferenceName: referenceName,
		SingleBranch:  referenceName != plumbing.ReferenceName(""),
		Depth:         depth,
		NoCheckout:    true,
		Tags:          git.AllTags,
		Auth:          authMethod,
	}
	repo, err := git.PlainCloneContext(ctx, destDir, false, cloneOptions)
	if err != nil {
		return friendlyCloneError(err)
	}

	hash, resolveErr := resolveRef(repo, ref)
	if resolveErr != nil {
		// A shallow clone does not contain every commit. Fetch the complete
		// history before reporting an otherwise valid commit SHA missing.
		if fetchErr := repo.FetchContext(ctx, &git.FetchOptions{
			RefSpecs: []config.RefSpec{"+refs/*:refs/*"},
			Tags:     git.AllTags,
			Auth:     authMethod,
		}); fetchErr != nil {
			return fmt.Errorf("fetch history for ref %q: %v: %w", ref, resolveErr, fetchErr)
		}
		hash, resolveErr = resolveRef(repo, ref)
	}
	if resolveErr != nil {
		return fmt.Errorf("resolve ref %q: %w", ref, resolveErr)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: *hash}); err != nil {
		return fmt.Errorf("checkout ref %q: %w", ref, err)
	}
	return nil
}

// cloneReferenceName returns a fully qualified branch or tag reference when
// it is unambiguous from the request. Short names need all remote refs cloned
// so that a tag is not mistaken for a branch; commit hashes are resolved after
// the clone as well.
func cloneReferenceName(ref string) (plumbing.ReferenceName, error) {
	if ref == "" {
		return plumbing.ReferenceName(""), fmt.Errorf("git ref cannot be empty")
	}
	if ref == "HEAD" || looksLikeCommitSHA(ref) {
		return plumbing.ReferenceName(""), nil
	}
	if !strings.HasPrefix(ref, "refs/") {
		if err := plumbing.NewBranchReferenceName(ref).Validate(); err != nil {
			return plumbing.ReferenceName(""), fmt.Errorf("invalid git ref %q: %w", ref, err)
		}
		return plumbing.ReferenceName(""), nil
	}

	name := plumbing.ReferenceName(ref)
	if err := name.Validate(); err != nil {
		return plumbing.ReferenceName(""), fmt.Errorf("invalid git ref %q: %w", ref, err)
	}
	if !name.IsBranch() && !name.IsTag() {
		return plumbing.ReferenceName(""), nil
	}
	return name, nil
}

func looksLikeCommitSHA(ref string) bool {
	if len(ref) < 7 || len(ref) > len(plumbing.ZeroHash)*2 {
		return false
	}
	for _, r := range ref {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func resolveRef(repo *git.Repository, ref string) (*plumbing.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err == nil || strings.HasPrefix(ref, "refs/") || looksLikeCommitSHA(ref) {
		if err == nil {
			return hash, nil
		}
		// go-git's revision resolver does not consistently accept abbreviated
		// object IDs after a shallow clone. Resolve the prefix against the
		// fetched commit objects so `git clone` and update support short SHAs
		// consistently.
		if looksLikeCommitSHA(ref) {
			commits, iterErr := repo.CommitObjects()
			if iterErr != nil {
				return nil, iterErr
			}
			defer commits.Close()
			var found *plumbing.Hash
			_ = commits.ForEach(func(commit *object.Commit) error {
				candidate := commit.Hash.String()
				if strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(ref)) {
					value := commit.Hash
					found = &value
				}
				return nil
			})
			if found != nil {
				return found, nil
			}
		}
		return nil, err
	}

	// A non-default branch may only exist as an origin/* remote-tracking ref
	// in a clone. Try that spelling before returning the original error.
	remoteHash, remoteErr := repo.ResolveRevision(plumbing.Revision("origin/" + ref))
	if remoteErr == nil {
		return remoteHash, nil
	}
	return hash, err
}

// Pull fetches the latest changes for an already cloned repo.
func Pull(ctx context.Context, dir, ref string, auth *Auth) error {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	return w.PullContext(ctx, &git.PullOptions{
		ReferenceName: plumbing.ReferenceName("refs/heads/" + ref),
		SingleBranch:  true,
		Depth:         1,
		Auth:          gitAuth(auth, repoURLFor(repo)),
	})
}

func repoURLFor(repo *git.Repository) string {
	if r, err := repo.Remote("origin"); err == nil {
		if len(r.Config().URLs) > 0 {
			return r.Config().URLs[0]
		}
	}
	return ""
}

func gitAuth(auth *Auth, rawURL string) transport.AuthMethod {
	if auth == nil || auth.Token == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	if u.Scheme == "ssh" || u.Scheme == "git" {
		return nil
	}
	user := auth.Username
	if user == "" {
		user = "x-access-token"
	}
	return &http.BasicAuth{Username: user, Password: auth.Token}
}

// FindComposeFile returns the path of the first compose file in dir,
// preferring modern (`compose.yml`) over legacy (`docker-compose.yml`).
func FindComposeFile(dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve source directory: %w", err)
	}
	candidates := []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}
	for _, name := range candidates {
		p := filepath.Join(root, name)
		info, statErr := os.Lstat(p)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("compose file %q must not be a symlink", p)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("compose file %q is not a regular file", p)
		}
		return p, nil
	}
	return "", fmt.Errorf("no compose file found in %s", dir)
}
