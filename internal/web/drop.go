package web

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

// extractAndDetect extracts a tarball (.tar.gz/.tgz) or zip into destDir.
// Returns the detected kind: "dockerfile" or "static".
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

func (c *extractCounters) addEntry(size int64) error {
	c.entries++
	if c.entries > extractMaxEntries {
		return fmt.Errorf("archive has too many entries (>%d)", extractMaxEntries)
	}
	if c.totalBytes+size > extractMaxTotalBytes {
		return fmt.Errorf("archive expands beyond %d bytes", extractMaxTotalBytes)
	}
	c.totalBytes += size
	return nil
}

func extractAndDetect(archivePath, destDir string) (string, error) {
	lower := strings.ToLower(archivePath)
	counters := &extractCounters{}
	if strings.HasSuffix(lower, ".zip") {
		if err := extractZip(archivePath, destDir, counters); err != nil {
			return "", err
		}
	} else {
		if err := extractTarGz(archivePath, destDir, counters); err != nil {
			return "", err
		}
	}
	return detectKind(destDir)
}

func detectKind(dir string) (string, error) {
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
		return "dockerfile", nil
	case hasStatic:
		return "static", nil
	default:
		return "", fmt.Errorf("could not detect tarball contents (no Dockerfile or static assets)")
	}
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
		case tar.TypeDir:
			if err := counters.addEntry(0); err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			size := hdr.Size
			if size > extractMaxFileBytes {
				return fmt.Errorf("archive file %q too large (%d bytes)", hdr.Name, size)
			}
			if err := counters.addEntry(size); err != nil {
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
			if _, err := io.Copy(out, io.LimitReader(tr, extractMaxFileBytes+1)); err != nil {
				out.Close()
				return err
			}
			out.Close()
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
		if f.FileInfo().IsDir() {
			if err := counters.addEntry(0); err != nil {
				return err
			}
			os.MkdirAll(target, 0o755)
			continue
		}
		if f.UncompressedSize64 > extractMaxFileBytes {
			return fmt.Errorf("archive file %q too large (%d bytes)", f.Name, f.UncompressedSize64)
		}
		if err := counters.addEntry(int64(f.UncompressedSize64)); err != nil {
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
		if _, err := io.Copy(out, io.LimitReader(src, extractMaxFileBytes+1)); err != nil {
			src.Close()
			out.Close()
			return err
		}
		src.Close()
		out.Close()
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	// Strip leading slashes and "./" prefixes; reject any ".." that survives.
	for {
		if strings.HasPrefix(name, "/") {
			name = name[1:]
			continue
		}
		if strings.HasPrefix(name, "./") {
			name = name[2:]
			continue
		}
		break
	}
	if name == "" || name == "." {
		return root, nil
	}
	if strings.Contains(name, "..") {
		return "", fmt.Errorf("illegal path in archive: %q", name)
	}
	target := filepath.Join(root, name)
	if !strings.HasPrefix(target, root+string(os.PathSeparator)) && target != root {
		return "", fmt.Errorf("illegal path in archive: %q", name)
	}
	return target, nil
}

func syntheticCompose(destDir, kind string) (string, error) {
	if kind != "dockerfile" && kind != "static" {
		return "", fmt.Errorf("unsupported drop kind %q", kind)
	}
	// "80" matches the default nginx:alpine listen port. Single-port spec
	// tells Podman to publish it to a random host port; combined with the
	// rootless gateway IP fallback in the Traefik writer, rootful Traefik
	// can reach the container via the host-published port.
	compose := `services:
  web:
    build: .
    ports:
      - "80"
`
	return compose, nil
}
