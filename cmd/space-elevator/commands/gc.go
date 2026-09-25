package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
)

var appsGcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage-collect orphaned drop source dirs, release trees, tarballs, and unused images",
	Long: `Sweep four things under the apps root and the local image store:

  - Drop dirs and tarballs with no matching row in the SQLite apps table
    (typical after a crash or partial remove).
  - Release trees under <apps_root>/releases whose app no longer exists,
    plus per-release checkouts older than the newest 3 for a live app.
    The current release is always retained regardless of age.
  - Local Podman images under localhost/se/* that are not referenced by
    any live app's compose spec or retained release image map.
  - Apps whose on-disk source directory has gone missing. These are
    reported, never deleted: the row still holds domains, secrets, and
    storage mappings that only the operator should discard.

The three most recent releases per app are retained so rollback stays
possible. Safe to run any time; idempotent.`,
	RunE: runAppsGc,
}

var (
	gcRemoveImages    bool
	gcRemoveOrphans   bool
	gcRemoveStubs     bool
	gcOrphanOlderThan time.Duration
)

func init() {
	appsCmd.AddCommand(appsGcCmd)
	f := appsGcCmd.Flags()
	f.BoolVar(&gcRemoveImages, "images", true, "remove unused localhost/se/* images")
	f.BoolVar(&gcRemoveOrphans, "orphans", true, "remove drop dirs/tarballs with no matching app row")
	f.BoolVar(&gcRemoveStubs, "stubs", true, "report apps whose source directory is missing")
	f.DurationVar(&gcOrphanOlderThan, "older-than", 1*time.Hour, "only remove orphan dirs/tarballs older than this (0 = any age)")
}

func runAppsGc(cmd *cobra.Command, _ []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()

	apps, err := st.ListApps(cmd.Context())
	if err != nil {
		return err
	}
	liveSources := map[string]bool{}
	liveAppIDs := map[string]bool{}
	liveReleaseDirs := map[string]bool{}
	releaseInventoryKnown := map[string]bool{}
	for _, a := range apps {
		liveAppIDs[a.ID] = true
		dir, derr := store.AppSourceDir(cfg.AppsRoot, a)
		if derr == nil {
			liveSources[dir] = true
		}
		if current, currentErr := st.GetCurrentRelease(cmd.Context(), a.ID); currentErr == nil && current.SourcePath != "" {
			liveSources[current.SourcePath] = true
		}
		if releases, releaseErr := st.ListReleases(cmd.Context(), a.ID); releaseErr == nil {
			releaseInventoryKnown[a.ID] = true
			for i, release := range releases {
				if i < 3 || release.ID == a.CurrentReleaseID {
					liveReleaseDirs[filepath.Join(cfg.AppsRoot, "releases", a.ID, release.ID)] = true
				}
			}
		}
	}

	// -- orphans --
	removedDirs := 0
	removedTars := 0
	if gcRemoveOrphans {
		entries, rerr := os.ReadDir(filepath.Join(cfg.AppsRoot, "drops"))
		if rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		for _, e := range entries {
			path := filepath.Join(cfg.AppsRoot, "drops", e.Name())
			if e.IsDir() {
				if liveSources[path] {
					continue
				}
				if !isOlderThan(path, gcOrphanOlderThan) {
					continue
				}
				if err := os.RemoveAll(path); err == nil {
					fmt.Printf("removed orphan dir: %s\n", path)
					removedDirs++
				}
			} else if strings.HasSuffix(e.Name(), ".tar.gz") {
				if !isOlderThan(path, gcOrphanOlderThan) {
					continue
				}
				if err := os.Remove(path); err == nil {
					fmt.Printf("removed orphan tarball: %s\n", path)
					removedTars++
				}
			}
		}
	}

	// -- orphaned release trees --
	removedReleases := 0
	if gcRemoveOrphans {
		releaseRoot := filepath.Join(cfg.AppsRoot, "releases")
		entries, rerr := os.ReadDir(releaseRoot)
		if rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			appID := entry.Name()
			path := filepath.Join(releaseRoot, appID)
			if !liveAppIDs[appID] {
				if !isOlderThan(path, gcOrphanOlderThan) {
					continue
				}
				if err := os.RemoveAll(path); err == nil {
					fmt.Printf("removed orphan release tree: %s\n", path)
					removedReleases++
				}
				continue
			}
			if !releaseInventoryKnown[appID] {
				continue
			}
			releaseEntries, readErr := os.ReadDir(path)
			if readErr != nil {
				continue
			}
			for _, releaseEntry := range releaseEntries {
				if !releaseEntry.IsDir() {
					continue
				}
				releasePath := filepath.Join(path, releaseEntry.Name())
				if liveReleaseDirs[releasePath] || !isOlderThan(releasePath, gcOrphanOlderThan) {
					continue
				}
				if err := os.RemoveAll(releasePath); err == nil {
					fmt.Printf("removed old release tree: %s\n", releasePath)
					removedReleases++
				}
			}
		}
	}

	// -- stubs --
	// A "stub" is an app row whose on-disk source directory is gone. There
	// is nothing safe to delete here: removing the row would drop the app's
	// domains, secrets, and storage mappings, and re-uploading is an
	// explicit operator action. So report them instead, which is what
	// actually helps: each line names an app that will fail to redeploy.
	stubbed := 0
	if gcRemoveStubs {
		for _, a := range apps {
			ok, err := store.SourceDirExists(cfg.AppsRoot, a)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: %s: %v\n", a.Name, err)
				continue
			}
			if ok {
				continue
			}
			dir, pathErr := store.AppSourceDir(cfg.AppsRoot, a)
			if pathErr != nil {
				continue
			}
			fmt.Printf("missing source for %s (%s); re-upload or redeploy to repair\n", a.Name, dir)
			stubbed++
		}
	}

	// -- images --
	removedImgs := 0
	if gcRemoveImages {
		// Build the set of live tags from compose specs.
		live := map[string]bool{}
		for _, a := range apps {
			spec, err := composer.Parse([]byte(a.ComposeYAML))
			if err == nil && spec != nil {
				for svcName, svc := range spec.Services {
					if svc.Image != "" {
						live[svc.Image] = true
					}
					if svc.Build != nil {
						// Mirror the runtime's stable slug-based tag convention.
						live["localhost/se/"+composer.SanitizeForImage(a.Slug)+"/"+composer.SanitizeForImage(svcName)+":latest"] = true
					}
				}
			}
			// Release images are immutable and are not necessarily present in
			// the app's current compose file. Retain the current release's
			// exact image map, and all known release maps while auditing.
			if releases, releaseErr := st.ListReleases(cmd.Context(), a.ID); releaseErr == nil {
				for i, release := range releases {
					if i >= 3 && release.ID != a.CurrentReleaseID {
						continue
					}
					for _, image := range release.ImageMap {
						if image != "" {
							live[image] = true
						}
					}
				}
			}
		}
		imgs, err := cli.ListImages(cmd.Context())
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: list images: %v\n", err)
		}
		for _, im := range imgs {
			for _, tag := range im.RepoTags {
				if !strings.HasPrefix(tag, "localhost/se/") {
					continue
				}
				if live[tag] {
					continue
				}
				if err := cli.RemoveImage(cmd.Context(), tag, false); err == nil {
					fmt.Printf("removed image: %s\n", tag)
					removedImgs++
				}
			}
		}
	}

	fmt.Printf("\nDone: dirs=%d tarballs=%d releases=%d images=%d\n", removedDirs, removedTars, removedReleases, removedImgs)
	if stubbed > 0 {
		fmt.Printf("warning: %d app(s) have a missing source directory; see above\n", stubbed)
	}
	return nil
}

// isOlderThan returns true if path's mtime is older than d. If d is 0,
// any existing path counts.
func isOlderThan(path string, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > d
}
