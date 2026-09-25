package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticBuildDockerfileAndCompose(t *testing.T) {
	build := StaticBuild{
		BuilderImage: "node:20-bookworm",
		BuildCommand: "npm ci && npm run build",
		ServePath:    "dist",
		ListenPort:   80,
		BaseHref:     "/app/site/",
	}
	if err := build.Validate(); err != nil {
		t.Fatal(err)
	}
	dockerfile := build.Dockerfile()
	for _, want := range []string{
		"FROM node:20-bookworm AS build",
		"RUN npm ci && npm run build",
		"FROM nginx:alpine",
		"COPY --from=build /app/dist/ /usr/share/nginx/html/",
		"EXPOSE 80",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("dockerfile missing %q:\n%s", want, dockerfile)
		}
	}
	if !strings.Contains(build.Compose(), `- "80"`) {
		t.Errorf("compose missing port:\n%s", build.Compose())
	}
}

func TestStaticBuildPortControlsNginx(t *testing.T) {
	build := StaticBuild{BuildCommand: "npm run build", ServePath: "dist", ListenPort: 3000}
	if err := build.Validate(); err != nil {
		t.Fatal(err)
	}
	conf := staticNginxConfPort(build.BaseHref, build.port())
	if !strings.Contains(conf, "listen       3000;") || !strings.Contains(conf, "listen  [::]:3000;") {
		t.Fatalf("nginx config does not use port 3000: %s", conf)
	}
}

func TestStaticBuildRejectsControlCharacterServePath(t *testing.T) {
	if err := (StaticBuild{ServePath: "dist\nRUN echo injected"}).Validate(); err == nil {
		t.Fatal("newline in serve path must be rejected")
	}
}

func TestStaticBuildDefaultsToRepositoryRoot(t *testing.T) {
	build := StaticBuild{BuildCommand: "npm run build"}
	if err := build.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(build.Dockerfile(), "COPY --from=build /app/ /usr/share/nginx/html/") {
		t.Errorf("root path copy missing:\n%s", build.Dockerfile())
	}
	if !strings.Contains(build.Dockerfile(), "FROM node:20-bookworm AS build") {
		t.Errorf("default builder image missing:\n%s", build.Dockerfile())
	}
}

func TestStaticBuildRejectsUnsafeServePath(t *testing.T) {
	for _, path := range []string{"../secrets", "/absolute", "dist\\public"} {
		if err := (StaticBuild{ServePath: path}).Validate(); err == nil {
			t.Errorf("path %q should be rejected", path)
		}
	}
}

func TestWriteStaticBuild(t *testing.T) {
	dir := t.TempDir()
	build := StaticBuild{BuildCommand: "npm run build", ServePath: "dist", BaseHref: "/app/site/"}
	if err := WriteStaticBuild(dir, build); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Dockerfile", "compose.yml", "nginx.conf", ".dockerignore"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	nginx, err := os.ReadFile(filepath.Join(dir, "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nginx), `<base href="/app/site/">`) {
		t.Errorf("nginx config missing base href:\n%s", nginx)
	}
}
