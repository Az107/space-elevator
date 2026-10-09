package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaterializeSource populates destDir from a local source that is either an
// archive (.tar.gz/.tgz/.zip) or a directory tree. It is the single entry
// point for turning a caller-provided source path into a deployable drop:
// the CLI may pass a directory or an archive, while the web/API always pass
// an uploaded archive.
func MaterializeSource(sourcePath, destDir string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyTree(sourcePath, destDir, &extractCounters{})
	}
	return ExtractArchive(sourcePath, destDir)
}

// copyTree copies a local directory tree into destDir with the same
// path-slip protection and expansion caps as archive extraction. Symlinks
// and other non-regular entries are rejected rather than followed, so a
// source folder cannot smuggle a host path into the drop.
func copyTree(src, dst string, counters *extractCounters) error {
	// Resolve the top-level argument so `upload ./link-to-site` works; symlinks
	// *inside* the tree are still rejected below.
	resolvedSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve source directory: %w", err)
	}
	absSrc, err := filepath.Abs(resolvedSrc)
	if err != nil {
		return err
	}
	if info, err := os.Stat(absSrc); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", src)
	}
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	// Refuse a destination nested inside the source: the walk would then
	// descend into the files it is writing.
	if rel, relErr := filepath.Rel(absSrc, absDst); relErr == nil {
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("destination %q is inside source %q", dst, src)
		}
	}

	return filepath.WalkDir(absSrc, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absSrc {
			return nil
		}
		rel, err := filepath.Rel(absSrc, path)
		if err != nil {
			return err
		}
		// DirEntry.Info is Lstat, so the symlink bit reflects the entry itself
		// rather than whatever it points at.
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains a symlink, which is not allowed: %q", rel)
		}
		target, err := safeJoin(absDst, rel)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := counters.addEntry(); err != nil {
				return err
			}
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			size := info.Size()
			if size > extractMaxFileBytes {
				return fmt.Errorf("source file %q too large (%d bytes)", rel, size)
			}
			if err := counters.addEntry(); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return copyRegularFile(path, target, info.Mode().Perm(), counters)
		default:
			return fmt.Errorf("source contains a non-regular file, which is not allowed: %q", rel)
		}
	})
}

func copyRegularFile(src, dst string, perm os.FileMode, counters *extractCounters) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	// Cap the copy: the stat size can lie for a file that grows mid-read.
	n, copyErr := copyArchiveData(out, in, extractMaxFileBytes)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return counters.addBytes(n)
}
