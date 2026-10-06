// Package selfupdate implements release-binary self-updates: discover a
// published GitHub release, verify the platform's binary against the
// release's checksums, install it atomically over the current binary,
// restart the systemd user unit and roll back on failure.
//
// Only GitHub Releases are implemented. The install shape mirrors
// RUNBOOK-actualizar-space-elevator.md: stage to a private file, verify the
// sha256, keep the previous binary as <target>.prev, rename into place and
// re-verify.
package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	defaultAPIBase = "https://api.github.com"
	defaultTimeout = 60 * time.Second
	maxJSONBytes   = 1 << 20
	// maxAssetBytes bounds a downloaded binary. The current binary is ~31 MB;
	// 256 MB leaves headroom while refusing an obviously wrong asset.
	maxAssetBytes = 256 << 20
	checksumName  = "checksums.txt"
)

var (
	// ErrNoRelease means the repository has no published, non-prerelease
	// release. GitHub returns 404 for that — and for drafts, which carry no
	// tag — so callers must treat it as "nothing to update to", not failure.
	ErrNoRelease = errors.New("no published release found")
	// ErrRateLimited means the GitHub API refused the request for quota.
	ErrRateLimited = errors.New("GitHub API rate limit exceeded")
	// ErrUnsupportedHost means update_repo pointed somewhere other than GitHub.
	ErrUnsupportedHost = errors.New("only GitHub releases are supported")
)

// Asset is a release attachment.
type Asset struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	APIURL     string `json:"url"`
	BrowserURL string `json:"browser_download_url"`
}

// Release is the subset of the GitHub release object we consume.
type Release struct {
	TagName string  `json:"tag_name"`
	Draft   bool    `json:"draft"`
	Assets  []Asset `json:"assets"`
}

// Client reads releases for one repository.
type Client struct {
	owner, repo  string
	apiBase      string
	token        string
	http         *http.Client
	GOOS, GOARCH string
}

// NewClient parses an update_repo value and returns a client for it. Accepted
// forms: https://github.com/owner/repo, git@github.com:owner/repo.git, or a
// bare owner/repo. An empty value is an error; callers decide whether the
// updater is configured.
//
// SPACE_ELEVATOR_UPDATE_API_BASE is an advanced override that points the API
// at a GitHub Enterprise instance or a local server (used by the hermetic
// tests). Setting it also lifts the github.com restriction on update_repo.
func NewClient(repoURL, token string) (*Client, error) {
	owner, repo, host, err := ParseRepo(repoURL)
	if err != nil {
		return nil, err
	}
	apiBase := defaultAPIBase
	if override := strings.TrimRight(strings.TrimSpace(os.Getenv("SPACE_ELEVATOR_UPDATE_API_BASE")), "/"); override != "" {
		apiBase = override
	} else if host != "github.com" && host != "api.github.com" {
		return nil, fmt.Errorf("%w: host %q", ErrUnsupportedHost, host)
	}
	return &Client{
		owner: owner, repo: repo,
		apiBase: apiBase,
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: defaultTimeout},
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}, nil
}

// ParseRepo extracts owner, repo and host from a repository reference.
func ParseRepo(raw string) (owner, repo, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errors.New("update repository is not configured")
	}
	if strings.HasPrefix(raw, "git@") {
		rest := strings.TrimPrefix(raw, "git@")
		i := strings.IndexByte(rest, ':')
		if i < 0 {
			return "", "", "", fmt.Errorf("cannot parse repository %q", raw)
		}
		host = rest[:i]
		owner, repo = splitOwnerRepo(rest[i+1:])
	} else if !strings.Contains(raw, "://") {
		// Bare owner/repo.
		host = "github.com"
		owner, repo = splitOwnerRepo(raw)
		if owner == "" {
			return "", "", "", fmt.Errorf("cannot parse repository %q: use owner/repo or a URL", raw)
		}
	} else {
		u, perr := url.Parse(raw)
		if perr != nil {
			return "", "", "", fmt.Errorf("invalid update repository %q: %w", raw, perr)
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return "", "", "", fmt.Errorf("update repository must use http(s), got %q", u.Scheme)
		}
		if u.User != nil {
			return "", "", "", errors.New("update repository must not contain credentials")
		}
		host = u.Hostname()
		owner, repo = splitOwnerRepo(strings.TrimPrefix(u.Path, "/"))
	}
	if owner == "" || repo == "" {
		return "", "", "", fmt.Errorf("cannot parse repository %q: need owner and repo", raw)
	}
	return owner, repo, host, nil
}

func splitOwnerRepo(p string) (string, string) {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// AssetName is the release asset name for a platform.
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("space-elevator-%s-%s", goos, goarch)
}

// Latest returns the newest published release.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.apiBase, c.owner, c.repo)
	var rel Release
	if err := c.getJSON(ctx, endpoint, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// FindAsset returns the named asset, or ErrNoRelease-style error when absent.
func FindAsset(rel *Release, name string) (*Asset, error) {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("release %s has no asset %q", rel.TagName, name)
}

// ExpectedSHA returns the sha256 that the release's checksums.txt records for
// assetName. A missing checksums asset or a missing entry is an error:
// an unverifiable binary is never installed.
func (c *Client) ExpectedSHA(ctx context.Context, rel *Release, assetName string) (string, error) {
	cs, err := FindAsset(rel, checksumName)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := c.DownloadAsset(ctx, cs, &buf); err != nil {
		return "", err
	}
	return ParseChecksums(buf.String(), assetName)
}

// ParseChecksums extracts the sha256 for name from sha256sum output.
func ParseChecksums(body, name string) (string, error) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		file := strings.TrimPrefix(fields[1], "*") // sha256sum binary-mode marker
		if file == name && len(sum) == 64 {
			return sum, nil
		}
	}
	return "", fmt.Errorf("%s has no entry for %s", checksumName, name)
}

// DownloadAsset streams an asset into w.
func (c *Client) DownloadAsset(ctx context.Context, a *Asset, w io.Writer) (int64, error) {
	endpoint := a.APIURL
	if endpoint == "" {
		endpoint = a.BrowserURL
	}
	resp, err := c.do(ctx, endpoint, "application/octet-stream")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if rateLimited(resp) {
			return 0, ErrRateLimited
		}
		return 0, fmt.Errorf("download %s: GitHub returned %s", a.Name, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return 0, fmt.Errorf("download %s: GitHub returned %s", a.Name, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return n, err
	}
	if n > maxAssetBytes {
		return n, fmt.Errorf("asset %s exceeds %d bytes", a.Name, maxAssetBytes)
	}
	return n, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, dst any) error {
	resp, err := c.do(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if rateLimited(resp) {
			return ErrRateLimited
		}
		return fmt.Errorf("GitHub API returned %s", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "space-elevator-selfupdate")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact GitHub: %w", err)
	}
	return resp, nil
}

func rateLimited(resp *http.Response) bool {
	return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
}
