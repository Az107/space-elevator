package deployer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func safeSourceRef(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
		return fallback
	}
	return value
}

// safeReleaseSource validates a persisted release source path before it is
// used as a build context or bind-mount root. Release rows are normally
// platform-created, but treating the database as untrusted prevents a
// tampered row from turning a redeploy into an arbitrary host-file read.
func (d *Deployer) safeReleaseSource(appID, path string) (string, error) {
	if path == "" || appID == "" || filepath.Base(appID) != appID || appID == "." || appID == ".." || strings.ContainsAny(appID, "\x00\r\n") {
		return "", fmt.Errorf("invalid release source identity")
	}
	root := filepath.Join(d.Opts.AppsRoot, "releases", appID)
	if info, err := os.Lstat(root); err != nil {
		return "", err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("release root is a symlink")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve release root: %w", err)
	}
	// Lstat above only inspects the leaf: it resolves any *intermediate*
	// symlink on the way to `releases/<appID>`. Swapping `<appsRoot>/releases`
	// for a symlink to `/` would therefore slip past it, and realRoot — the
	// containment root for every comparison below — would point outside the
	// apps tree entirely. Re-anchor containment on the real apps root so a
	// symlinked `releases` cannot redefine what "inside" means.
	realAppsRoot, err := filepath.EvalSymlinks(d.Opts.AppsRoot)
	if err != nil {
		return "", fmt.Errorf("resolve apps root: %w", err)
	}
	if rel, relErr := filepath.Rel(realAppsRoot, realRoot); relErr != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("release root escapes apps root")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(realPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("release source is not a directory")
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("release source escapes app release root")
	}
	releaseID := strings.Split(rel, string(filepath.Separator))[0]
	if err := validateReleaseID(releaseID); err != nil {
		return "", err
	}
	return realPath, nil
}
