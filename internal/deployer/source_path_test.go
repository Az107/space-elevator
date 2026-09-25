package deployer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSafeReleaseSourceRejectsNonReleasePaths documents the invariant that a
// redeploy must respect: a release SourcePath is only usable if it lives under
// <appsRoot>/releases/<appID>/<releaseID>.
//
// This is the exact shape a redeploy would record if it tried to treat the
// live checkout (sources/<appID>) as an immutable release. update() treats a
// rejection as fatal, so recording one permanently bricks every later update
// for that app — which is why redeploy clears the current release pointer
// instead of minting a row.
func TestSafeReleaseSourceRejectsNonReleasePaths(t *testing.T) {
	appsRoot := t.TempDir()
	appID := "app-1"
	releaseID := "rel-1"

	releaseRoot := filepath.Join(appsRoot, "releases", appID)
	good := filepath.Join(releaseRoot, releaseID)
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	// A live checkout, which is what redeploy works from.
	liveCheckout := filepath.Join(appsRoot, "sources", appID)
	if err := os.MkdirAll(liveCheckout, 0o755); err != nil {
		t.Fatal(err)
	}
	// A drop directory, likewise outside the release tree.
	drop := filepath.Join(appsRoot, "drops", appID)
	if err := os.MkdirAll(drop, 0o755); err != nil {
		t.Fatal(err)
	}

	d := &Deployer{Opts: Options{AppsRoot: appsRoot}}

	if got, err := d.safeReleaseSource(appID, good); err != nil || got == "" {
		t.Fatalf("valid release source rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"live checkout", liveCheckout},
		{"drop dir", drop},
		{"apps root itself", appsRoot},
		{"outside the tree", t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.safeReleaseSource(appID, tc.path); err == nil {
				t.Fatalf("path outside the release tree was accepted: %q", tc.path)
			}
		})
	}
}

// TestSafeReleaseSourceRejectsEscapingSymlinkedRoot guards the containment
// check against a release root that has been swapped for a symlink pointing
// outside the apps tree. Lstat on `releases/<appID>` resolves the
// intermediate symlink, so only re-anchoring on the real apps root catches
// this.
func TestSafeReleaseSourceRejectsEscapingSymlinkedRoot(t *testing.T) {
	appsRoot := t.TempDir()
	appID := "app-1"

	// A release tree planted entirely outside appsRoot.
	outside := t.TempDir()
	planted := filepath.Join(outside, appID, "rel-1")
	if err := os.MkdirAll(planted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(appsRoot, "releases")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	d := &Deployer{Opts: Options{AppsRoot: appsRoot}}
	if _, err := d.safeReleaseSource(appID, planted); err == nil {
		t.Fatal("release root symlinked outside the apps tree was accepted")
	}
}
