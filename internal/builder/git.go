package builder

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

type Auth struct {
	Username string
	Token    string
}

// Clone clones a git repo into destDir and checks out the given ref (branch/tag/commit).
func Clone(ctx context.Context, repoURL, ref, destDir string, auth *Auth) error {
	_, err := git.PlainCloneContext(ctx, destDir, false, &git.CloneOptions{
		URL:        repoURL,
		ReferenceName: plumbing.ReferenceName("refs/heads/" + ref),
		SingleBranch: true,
		Depth:        1,
		Auth:        gitAuth(auth, repoURL),
	})
	return err
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
		SingleBranch: true,
		Depth:        1,
		Auth:        gitAuth(auth, repoURLFor(repo)),
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
	candidates := []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s", dir)
}