package builder

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed function_templates/python/server.py function_templates/node/server.mjs
var functionTemplates embed.FS

// Function language identifiers.
const (
	LanguagePython = "python"
	LanguageNode   = "node"
)

// DefaultFunctionPort is the port the generated adapter listens on. It
// matches DefaultListenPort so routing/limits behave like every other
// synthesized app.
const DefaultFunctionPort = 8080

var (
	entryFileRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	entrySymbolRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	versionRe     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// FunctionBuild describes a serverless-style function deploy: the
// platform wraps the user's handler in a generated HTTP adapter and
// synthesizes a single-stage Dockerfile, so functions flow through the
// exact same compose→build→run→route pipeline as everything else.
//
// The image is built to start fast and statelessly, and the adapter
// exposes /__se/health, so a future scale-to-zero activator can start a
// stopped container on demand without changing the build output.
type FunctionBuild struct {
	// Language is "python" or "node". Required.
	Language string
	// Version is the base image tag (e.g. "3.12", "20"). Defaults per
	// language when empty.
	Version string
	// Entrypoint is "file:symbol" (e.g. "handler.py:handler",
	// "app/main.py:handler", "index.js:handler"). Defaults per language.
	Entrypoint string
	// EnvKeys lists plain env var names, declared as Dockerfile ARGs so
	// build-time reads (e.g. inlined config) see them. Secrets excluded.
	EnvKeys []string
}

// DefaultVersion returns the fallback base image version for a language.
func DefaultVersion(language string) string {
	switch normalizeLanguage(language) {
	case LanguageNode:
		return "20"
	default:
		return "3.12"
	}
}

// DefaultEntrypoint returns the conventional entrypoint for a language.
func DefaultEntrypoint(language string) string {
	switch normalizeLanguage(language) {
	case LanguageNode:
		return "index.js:handler"
	default:
		return "handler.py:handler"
	}
}

func normalizeLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case LanguagePython, "py":
		return LanguagePython
	case LanguageNode, "nodejs", "javascript", "js":
		return LanguageNode
	default:
		return ""
	}
}

func (f FunctionBuild) language() string { return normalizeLanguage(f.Language) }

func (f FunctionBuild) version() string {
	v := strings.TrimSpace(f.Version)
	if v == "" {
		return DefaultVersion(f.Language)
	}
	return v
}

func (f FunctionBuild) entrypoint() string {
	e := strings.TrimSpace(f.Entrypoint)
	if e == "" {
		return DefaultEntrypoint(f.Language)
	}
	return e
}

// ParseEntrypoint splits "file:symbol" into its parts, applying the
// language default symbol when omitted. It does not validate.
func ParseEntrypoint(language, entry string) (file, symbol string) {
	if strings.TrimSpace(entry) == "" {
		entry = DefaultEntrypoint(language)
	}
	if i := strings.LastIndex(entry, ":"); i >= 0 {
		return entry[:i], entry[i+1:]
	}
	return entry, "handler"
}

// Validate checks the fields the synthesizer can't invent and rejects
// anything that could escape the build context or inject Dockerfile
// directives.
func (f FunctionBuild) Validate() error {
	lang := f.language()
	if lang == "" {
		return fmt.Errorf("language must be python or node")
	}
	if !versionRe.MatchString(f.version()) {
		return fmt.Errorf("runtime version %q contains unexpected characters", f.Version)
	}
	file, symbol := ParseEntrypoint(lang, f.entrypoint())
	if file == "" || !entryFileRe.MatchString(file) {
		return fmt.Errorf("entrypoint file %q is not a valid relative path", file)
	}
	if strings.HasPrefix(file, "/") || strings.Contains(file, "\\") {
		return fmt.Errorf("entrypoint file %q must be a relative path", file)
	}
	for _, part := range strings.Split(file, "/") {
		if part == ".." || part == "." || part == "" {
			return fmt.Errorf("entrypoint file %q may not contain empty or relative path segments", file)
		}
	}
	if !entrySymbolRe.MatchString(symbol) {
		return fmt.Errorf("handler name %q is not a valid identifier", symbol)
	}
	return nil
}

// Dockerfile renders the single-stage function image. The adapter is
// copied from the build context (.se-function) and dependencies are
// installed at build time so the container starts fast.
func (f FunctionBuild) Dockerfile() string {
	lang := f.language()
	var b strings.Builder
	switch lang {
	case LanguageNode:
		fmt.Fprintf(&b, "FROM node:%s-slim\n", f.version())
	default:
		fmt.Fprintf(&b, "FROM python:%s-slim\n", f.version())
	}
	for _, k := range f.EnvKeys {
		fmt.Fprintf(&b, "ARG %s\n", k)
	}
	fmt.Fprintf(&b, "ENV SE_FUNCTION_ENTRYPOINT=%q \\\n    SE_FUNCTION_APPDIR=\"/app\" \\\n    PORT=\"%d\"\n",
		f.entrypoint(), DefaultFunctionPort)
	b.WriteString("WORKDIR /app\n")
	b.WriteString("COPY . .\n")
	switch lang {
	case LanguageNode:
		b.WriteString("RUN if [ -f package.json ]; then npm ci || npm install; fi\n")
		b.WriteString("COPY .se-function/server.mjs /opt/function/server.mjs\n")
		b.WriteString("EXPOSE " + fmt.Sprint(DefaultFunctionPort) + "\n")
		b.WriteString("CMD [\"node\", \"/opt/function/server.mjs\"]\n")
	default:
		b.WriteString("RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi\n")
		b.WriteString("COPY .se-function/server.py /opt/function/server.py\n")
		b.WriteString("EXPOSE " + fmt.Sprint(DefaultFunctionPort) + "\n")
		b.WriteString("CMD [\"python\", \"/opt/function/server.py\"]\n")
	}
	return b.String()
}

// Compose renders the synthetic single-service compose file. The service
// is named "web" so Traefik path-prefix routing is uniform across every
// app kind.
func (f FunctionBuild) Compose() string {
	return fmt.Sprintf(`services:
  web:
    build: .
    ports:
      - "%d"
`, DefaultFunctionPort)
}

// WriteFunctionBuild writes the synthesized Dockerfile, compose.yml,
// .dockerignore, and the language adapter into destDir. It overwrites
// previous output so redeploys pick up edited settings and adapter
// changes.
func WriteFunctionBuild(destDir string, f FunctionBuild) error {
	if err := f.Validate(); err != nil {
		return err
	}
	adapterDir := filepath.Join(destDir, ".se-function")
	if err := os.MkdirAll(adapterDir, 0o755); err != nil {
		return fmt.Errorf("create adapter dir: %w", err)
	}
	adapterSrc, adapterName, err := adapterFor(f.language())
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(adapterDir, adapterName), adapterSrc, 0o644); err != nil {
		return fmt.Errorf("write adapter: %w", err)
	}
	files := map[string]string{
		"Dockerfile":    f.Dockerfile(),
		"compose.yml":   f.Compose(),
		".dockerignore": ".git\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(destDir, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func adapterFor(language string) ([]byte, string, error) {
	switch language {
	case LanguageNode:
		b, err := functionTemplates.ReadFile("function_templates/node/server.mjs")
		return b, "server.mjs", err
	case LanguagePython:
		b, err := functionTemplates.ReadFile("function_templates/python/server.py")
		return b, "server.py", err
	default:
		return nil, "", fmt.Errorf("unsupported language %q", language)
	}
}
