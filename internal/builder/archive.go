package builder

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Drop kinds detected from an extracted archive.
const (
	DropKindStatic     = "static"
	DropKindDockerfile = "dockerfile"
)

// Extraction caps: a malicious or accidental archive must not be able to
// exhaust the host's disk or inode table.
const (
	extractMaxEntries    = 10_000
	extractMaxTotalBytes = 512 << 20 // 512 MiB uncompressed
	extractMaxFileBytes  = 128 << 20 // per-file
)

// extractCounters tracks archive expansion against the caps above.
type extractCounters struct {
	entries    int
	totalBytes int64
}

func (c *extractCounters) addEntry() error {
	c.entries++
	if c.entries > extractMaxEntries {
		return fmt.Errorf("archive has too many entries (>%d)", extractMaxEntries)
	}
	return nil
}

func (c *extractCounters) addBytes(size int64) error {
	if size < 0 || c.totalBytes > extractMaxTotalBytes-size {
		return fmt.Errorf("archive expands beyond %d bytes", extractMaxTotalBytes)
	}
	c.totalBytes += size
	return nil
}

// ExtractArchive extracts a tarball (.tar.gz/.tgz) or zip into destDir.
// Paths are confined to destDir (zip-slip safe) and expansion is capped.
func ExtractArchive(archivePath, destDir string) error {
	lower := strings.ToLower(archivePath)
	counters := &extractCounters{}
	if strings.HasSuffix(lower, ".zip") {
		return extractZip(archivePath, destDir, counters)
	}
	return extractTarGz(archivePath, destDir, counters)
}

// DetectKind inspects an extracted source tree and returns the drop kind:
// "dockerfile" when a Dockerfile is present, else "static" when web
// assets are found.
func DetectKind(dir string) (string, error) {
	hasDockerfile := false
	hasStatic := false
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if name == "Dockerfile" || strings.HasSuffix(strings.ToLower(name), ".dockerfile") {
			hasDockerfile = true
		}
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".css") ||
			strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".json") ||
			strings.HasSuffix(lower, ".svg") || strings.HasSuffix(lower, ".png") ||
			strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
			strings.HasSuffix(lower, ".gif") || strings.HasSuffix(lower, ".webp") ||
			strings.HasSuffix(lower, ".woff") || strings.HasSuffix(lower, ".woff2") {
			hasStatic = true
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	switch {
	case hasDockerfile:
		return DropKindDockerfile, nil
	case hasStatic:
		return DropKindStatic, nil
	default:
		return "", fmt.Errorf("could not detect tarball contents (no Dockerfile or static assets)")
	}
}

// HasDockerfile reports whether dir (recursively) contains a Dockerfile.
func HasDockerfile(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if name == "Dockerfile" || strings.HasSuffix(strings.ToLower(name), ".dockerfile") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// SyntheticCompose is the single-service compose used for archive drops.
// "80" matches the default nginx:alpine listen port; the bare port spec
// publishes a random host port that the Traefik resolver picks up via
// InspectIPs.
func SyntheticCompose() string {
	return `services:
  web:
    build: .
    ports:
      - "80"
`
}

func extractTarGz(path, dest string, counters *extractCounters) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive symlinks and hard links are not allowed: %q", hdr.Name)
		case tar.TypeDir:
			if err := counters.addEntry(); err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			size := hdr.Size
			if size < 0 {
				return fmt.Errorf("archive file %q has a negative size", hdr.Name)
			}
			if size > extractMaxFileBytes {
				return fmt.Errorf("archive file %q too large (%d bytes)", hdr.Name, size)
			}
			if err := counters.addEntry(); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			// Cap the copy: header size is untrusted for gzip streams.
			n, copyErr := copyArchiveData(out, tr, extractMaxFileBytes)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := counters.addBytes(n); err != nil {
				return err
			}
		}
	}
}

func extractZip(path, dest string, counters *extractCounters) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive symlinks are not allowed: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := counters.addEntry(); err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.UncompressedSize64 > extractMaxFileBytes {
			return fmt.Errorf("archive file %q too large (%d bytes)", f.Name, f.UncompressedSize64)
		}
		if err := counters.addEntry(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		// Cap the copy: the central directory size can lie for bombs.
		n, copyErr := copyArchiveData(out, src, extractMaxFileBytes)
		srcCloseErr := src.Close()
		outCloseErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if srcCloseErr != nil {
			return srcCloseErr
		}
		if outCloseErr != nil {
			return outCloseErr
		}
		if err := counters.addBytes(n); err != nil {
			return err
		}
	}
	return nil
}

func copyArchiveData(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("archive file exceeds %d bytes", limit)
	}
	return n, nil
}

func safeJoin(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "."
	}
	if strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", fmt.Errorf("illegal path in archive: %q", name)
	}
	name = filepath.Clean(filepath.FromSlash(name))
	if name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("illegal path in archive: %q", name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Lstat(rootAbs); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("archive destination root is a symlink")
		}
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	target, err := filepath.Abs(filepath.Join(rootAbs, name))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("illegal path in archive: %q", name)
	}
	// The destination is normally new, but callers may reuse a directory.
	// Refuse pre-existing symlink components so a later archive member cannot
	// escape through one.
	if err := rejectSymlinkComponents(rootAbs, target); err != nil {
		return "", err
	}
	return target, nil
}

func rejectSymlinkComponents(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive destination contains symlink %q", current)
		}
	}
	return nil
}
