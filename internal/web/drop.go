package web

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractAndDetect extracts a tarball (.tar.gz/.tgz) or zip into destDir.
// Returns the detected kind: "dockerfile" or "static".
func extractAndDetect(archivePath, destDir string) (string, error) {
	lower := strings.ToLower(archivePath)
	if strings.HasSuffix(lower, ".zip") {
		if err := extractZip(archivePath, destDir); err != nil {
			return "", err
		}
	} else {
		if err := extractTarGz(archivePath, destDir); err != nil {
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

func extractTarGz(path, dest string) error {
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
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func extractZip(path, dest string) error {
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
			os.MkdirAll(target, 0o755)
			continue
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
		if _, err := io.Copy(out, src); err != nil {
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

// wellKnownBuildDirs is the order we check for nested build output when a
// tarball doesn't have an index.html at the root. _site first because
// that's what Jekyll and Eleventy emit.
var wellKnownBuildDirs = []string{
	"_site", "dist", "public", "build", "out", "output",
}

// staticDockerfile generates the Dockerfile + nginx config for a static
// drop, choosing the right nginx doc root:
//
//  1. If destDir/index.html exists → /usr/share/nginx/html is the root.
//  2. Else, if a well-known build subdir (_site, dist, public, …) has an
//     index.html, that subdir becomes the root.
//  3. Else, if exactly one immediate subdir of destDir contains an
//     index.html, that subdir becomes the root. (Common when a user zips
//     "mysite/" and the folder itself is the only top-level entry.)
//  4. Otherwise, fall back to root and let the user see the default page.
//
// baseHref is the URL prefix the app is served at (e.g. "/app/foo/" for the
// default path-prefix route, or "/" when served from a custom subdomain).
// It is injected as <base href="..."> into every HTML response so absolute
// paths like "/dist/css/app.css" resolve correctly under the prefix.
//
// WriteStaticFiles writes both Dockerfile and (when needed) nginx.conf into
// destDir so the synth compose can build from them. Exported so the CLI's
// `apps redeploy` can re-run the synth against the still-extracted source.
func WriteStaticFiles(destDir, baseHref string) (string, error) {
	root := detectStaticRoot(destDir)
	var dockerfile string
	if root == "" || root == "." {
		dockerfile = "FROM nginx:alpine\nCOPY . /usr/share/nginx/html\n"
	} else {
		dockerfile = fmt.Sprintf(`FROM nginx:alpine
COPY %s /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
`, root) + "\n"
		conf := staticNginxConf(baseHref)
		if err := os.WriteFile(filepath.Join(destDir, "nginx.conf"), []byte(conf), 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(destDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return "", err
	}
	return root, nil
}

func staticNginxConf(baseHref string) string {
	// sub_filter injects <base href="..."> right after <head> so the
	// browser resolves absolute paths (/dist/css/app.css) against the
	// app's mount prefix instead of the host root. When the app is
	// served at the subdomain root (baseHref == "/"), there's nothing
	// useful to inject — skip the sub_filter block entirely so we don't
	// ship a config that confuses operators reading it.
	body := `server {
    listen       80;
    listen  [::]:80;
    server_name  _;

    root   /usr/share/nginx/html;
    index  index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }
`
	if baseHref != "" && baseHref != "/" {
		// Escape any double quotes for safety; baseHref is a URL prefix
		// that we control (constructed from app name + configured prefix).
		safe := strings.ReplaceAll(baseHref, `"`, `\"`)
		// sub_filter_once off covers HTML pages that mention <head> more
		// than once (rare).
		body += fmt.Sprintf(`
    sub_filter '<head>' '<head><base href="%s">';
    sub_filter_once off;
    sub_filter_types text/html;
`, safe)
	}
	return body + "}\n"
}

// detectStaticRoot returns the subdirectory (relative to destDir) that
// should serve as the nginx doc root, or "" if no good candidate exists.
// "." means "destDir itself".
func detectStaticRoot(destDir string) string {
	if hasIndex(filepath.Join(destDir, "index.html")) {
		return "."
	}
	for _, name := range wellKnownBuildDirs {
		if hasIndex(filepath.Join(destDir, name, "index.html")) {
			return name
		}
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return ""
	}
	var hits []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if hasIndex(filepath.Join(destDir, e.Name(), "index.html")) {
			hits = append(hits, e.Name())
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return ""
}

func hasIndex(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// _ = bytes.NewReader is referenced for any future gzipped-tar helpers.
var _ = bytes.NewReader