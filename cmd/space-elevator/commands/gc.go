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
	Short: "Garbage-collect orphaned drop source dirs, tarballs, and unused images",
	Long: `Sweep three things:

  - Drop dirs and tarballs under appsRoot that no longer have a matching
    row in the SQLite apps table (typical after a crash or partial remove).
  - Drop dirs whose DB row exists but the directory is missing (cleanup of
    the empty stubs a partial upload leaves behind).
  - Local Podman images under localhost/se/* that aren't referenced by
    any current app's compose spec.

Safe to run any time; idempotent.`,
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
	f.BoolVar(&gcRemoveStubs, "stubs", true, "remove empty drop dirs whose app exists but source is missing")
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
	for _, a := range apps {
		dir, derr := store.AppSourceDir(cfg.AppsRoot, a)
		if derr != nil {
			continue
		}
		liveSources[dir] = true
		// Spec-driven image refs: every service in the compose file
		// either builds from a tag we know, or pulls an image. We can
		// only safely clean what we built.
		// (Image cleanup below handles the localhost/se/* tagspace.)
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

	// -- stubs --
	if gcRemoveStubs {
		for _, a := range apps {
			ok, _ := store.SourceDirExists(cfg.AppsRoot, a)
			if !ok {
				fmt.Printf("kept stub row: %s (source dir missing, leaving DB row for manual fix)\n", a.Name)
			}
		}
	}

	// -- images --
	removedImgs := 0
	if gcRemoveImages {
		// Build the set of live tags from compose specs.
		live := map[string]bool{}
		for _, a := range apps {
			spec, err := composerParseOrSkip(a.ComposeYAML)
			if err != nil || spec == nil {
				continue
			}
			for svcName, svc := range spec.Services {
				if svc.Image != "" {
					live[svc.Image] = true
				}
				if svc.Build != nil {
					// Mirror the runtime's tag convention.
					live["localhost/se/"+composer.SanitizeForImage(a.Name)+"/"+composer.SanitizeForImage(svcName)+":latest"] = true
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

	fmt.Printf("\nDone: dirs=%d tarballs=%d images=%d\n", removedDirs, removedTars, removedImgs)
	return nil
}

// isOlderThan returns true if path's mtime is older than d. If d is 0,
// any existing path counts.
func isOlderThan(path string, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > d
}

// composerParseOrSkip wraps composer.Parse so gc can skip apps with
// corrupt stored compose instead of failing the whole sweep.
func composerParseOrSkip(raw string) (*composer.Spec, error) {
	return composer.Parse([]byte(raw))
}
