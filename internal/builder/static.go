package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// wellKnownBuildDirs is the order we check for nested build output when a
// source tree doesn't have an index.html at the root. _site first because
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
// destDir so the synth compose can build from them. Lives in builder (not
// web) because the deployer and the CLI both regenerate it on redeploy.
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
