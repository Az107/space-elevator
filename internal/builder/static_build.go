package builder

import (
	"fmt"
	"strings"
)

// StaticBuild describes a Git web app that is built in a tool image and
// then served by the platform's Nginx image. ServePath is relative to the
// repository root and is the directory copied into Nginx's document root.
type StaticBuild struct {
	BuilderImage string
	BuildCommand string
	ServePath    string
	ListenPort   int
	BaseHref     string
	EnvKeys      []string
}

const (
	DefaultStaticListenPort = 80
	defaultStaticBuilder    = "node:20-bookworm"
)

func (s StaticBuild) image() string {
	if image := strings.TrimSpace(s.BuilderImage); image != "" {
		return image
	}
	if strings.TrimSpace(s.BuildCommand) == "" {
		return "nginx:alpine"
	}
	return defaultStaticBuilder
}

func (s StaticBuild) port() int {
	if s.ListenPort <= 0 {
		return DefaultStaticListenPort
	}
	return s.ListenPort
}

func (s StaticBuild) path() string {
	path := strings.TrimSpace(strings.ReplaceAll(s.ServePath, "\\", "/"))
	if path == "" || path == "." {
		return "."
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return ""
		}
	}
	if strings.HasPrefix(path, "/") {
		return ""
	}
	return strings.Trim(path, "/")
}

func (s StaticBuild) Validate() error {
	if strings.ContainsAny(s.image(), " \t\n;$&|<>`") {
		return fmt.Errorf("builder image %q contains unexpected characters", s.image())
	}
	if p := s.port(); p < 1 || p > 65535 {
		return fmt.Errorf("listen port %d out of range (1-65535)", p)
	}
	rawPath := strings.TrimSpace(s.ServePath)
	if rawPath != "" && rawPath != "." && s.path() == "" {
		return fmt.Errorf("serve path %q must be a relative repository path without '..'", rawPath)
	}
	if strings.Contains(rawPath, "\\") {
		return fmt.Errorf("serve path %q must use '/' separators", rawPath)
	}
	if strings.ContainsAny(rawPath, "\x00\r\n") {
		return fmt.Errorf("serve path %q contains a control character", rawPath)
	}
	return nil
}

func (s StaticBuild) Dockerfile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s AS build\n", s.image())
	for _, key := range s.EnvKeys {
		fmt.Fprintf(&b, "ARG %s\n", key)
	}
	b.WriteString("WORKDIR /app\n")
	b.WriteString("COPY . .\n")
	if command := strings.TrimSpace(s.BuildCommand); command != "" {
		b.WriteString("RUN " + command + "\n")
	}
	b.WriteString("FROM nginx:alpine\n")
	b.WriteString("COPY nginx.conf /etc/nginx/conf.d/default.conf\n")
	path := s.path()
	if path == "." {
		b.WriteString("COPY --from=build /app/ /usr/share/nginx/html/\n")
	} else {
		fmt.Fprintf(&b, "COPY --from=build /app/%s/ /usr/share/nginx/html/\n", path)
	}
	b.WriteString("EXPOSE 80\n")
	return b.String()
}

func (s StaticBuild) Compose() string {
	return fmt.Sprintf(`services:
  web:
    build: .
    ports:
      - "%d"
`, s.port())
}

// WriteStaticBuild writes a multi-stage build image and the platform's
// Nginx configuration. The final image contains only the selected output
// directory, so users do not need to install or invoke http-server.
func WriteStaticBuild(destDir string, build StaticBuild) error {
	if err := build.Validate(); err != nil {
		return err
	}
	files := map[string]string{
		"Dockerfile":    build.Dockerfile(),
		"compose.yml":   build.Compose(),
		"nginx.conf":    staticNginxConfPort(build.BaseHref, build.port()),
		".dockerignore": ".git\n",
	}
	for name, body := range files {
		if err := writeGeneratedFile(destDir, name, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
