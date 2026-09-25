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
// WriteStaticFiles writes both Dockerfile and nginx.conf into destDir so
// the synth compose can build from them. The optional port is used by
// callers that publish a non-default container port; archive drops retain
// the historical port 80 default.
func WriteStaticFiles(destDir, baseHref string, ports ...int) (string, error) {
	port := 80
	if len(ports) > 0 && ports[0] > 0 {
		port = ports[0]
	}
	root := detectStaticRoot(destDir)
	var dockerfile string
	if root == "" || root == "." {
		dockerfile = "FROM nginx:alpine\nCOPY . /usr/share/nginx/html\nCOPY nginx.conf /etc/nginx/conf.d/default.conf\n"
	} else {
		dockerfile = fmt.Sprintf(`FROM nginx:alpine
COPY %s /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
`, root) + "\n"
	}
	if err := writeGeneratedFile(destDir, "nginx.conf", []byte(staticNginxConfPort(baseHref, port)), 0o644); err != nil {
		return "", err
	}
	if err := writeGeneratedFile(destDir, "Dockerfile", []byte(dockerfile), 0o644); err != nil {
		return "", err
	}
	return root, nil
}

func staticNginxConfPort(baseHref string, port int) string {
	// sub_filter injects <base href="..."> right after <head> so the
	// browser resolves absolute paths (/dist/css/app.css) against the
	// app's mount prefix instead of the host root. When the app is
	// served at the subdomain root (baseHref == "/"), there's nothing
	// useful to inject — skip the sub_filter block entirely so we don't
	// ship a config that confuses operators reading it.
	body := fmt.Sprintf(`server {
    listen       %d;
    listen  [::]:%d;
    server_name  _;

    root   /usr/share/nginx/html;
    index  index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }
`, port, port)
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
