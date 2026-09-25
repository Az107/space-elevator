package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeGeneratedFile replaces a platform-generated file without following a
// pre-existing symlink. Repositories are untrusted input, so a checkout must
// not be able to turn a generated Dockerfile or compose file into a write
// primitive against the host filesystem.
func writeGeneratedFile(destDir, name string, data []byte, perm os.FileMode) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\x00\r\n") {
		return fmt.Errorf("invalid generated filename %q", name)
	}
	if info, err := os.Lstat(destDir); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination directory is a symlink")
	}
	realDir, err := filepath.EvalSymlinks(destDir)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}
	path := filepath.Join(realDir, name)
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symlink %q", path)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}

	tmp, err := os.CreateTemp(realDir, ".space-elevator-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
