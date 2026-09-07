package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SourceDirExists reports whether the on-disk source dir for an app is
// still present. Used by the GC sweep to distinguish apps that have a
// backing directory from apps that were partially cleaned up.
func SourceDirExists(appsRoot string, a *App) (bool, error) {
	dir, err := AppSourceDir(appsRoot, a)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

// DeleteAppArtifacts removes everything on disk that an app owns: the
// extracted source dir, the dropped tarball (if any), and (via the caller)
// the local image. Idempotent — safe to call when files are already gone.
func DeleteAppArtifacts(appsRoot string, a *App) error {
	dir, err := AppSourceDir(appsRoot, a)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove source dir: %w", err)
	}
	// Tarball lives next to the source dir under a fixed name.
	tar := strings.TrimSuffix(dir, filepath.Base(dir)) + a.ID + ".tar.gz"
	if err := os.Remove(tar); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove tarball: %w", err)
	}
	return nil
}
