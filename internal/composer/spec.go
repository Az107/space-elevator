package composer

import "gopkg.in/yaml.v3"

type Spec struct {
	Version  string             `yaml:"version"`
	Services map[string]Service `yaml:"services"`
	Networks map[string]Network `yaml:"networks,omitempty"`
	Volumes  map[string]Volume  `yaml:"volumes,omitempty"`
}

type BuildConfig struct {
	Context    string `yaml:"context,omitempty"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
	Tag        string `yaml:"-"`
}

// UnmarshalYAML accepts either a string shorthand ("build: .") or a full
// object ("build: { context: ., dockerfile: Dockerfile }"). The string
// shorthand sets Context.
func (b *BuildConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		b.Context = value.Value
		return nil
	}
	type raw BuildConfig
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*b = BuildConfig(r)
	return nil
}

type Service struct {
	Image       string            `yaml:"image,omitempty"`
	Build       *BuildConfig      `yaml:"build,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	Networks    []string          `yaml:"networks,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
}

type Network struct {
	Driver string `yaml:"driver,omitempty"`
}

type Volume struct {
	Driver string `yaml:"driver,omitempty"`
}

type AppMeta struct {
	ID    string
	Name  string
	Env   map[string]string
	Label string
	// BuildEnv is the app's plain env, exposed to image builds: as
	// --build-arg values and as a managed .env.local in the build
	// context (so Vite/Next/CRA-style bundlers inline them at build
	// time). Secrets deliberately excluded — build args and context
	// files leak into image layers/metadata.
	BuildEnv map[string]string
	// StaticDrop is true for synth nginx:alpine drops that can safely
	// run with a read-only root FS. Callers set it from the App.DropKind
	// in the store. When false, the runtime leaves the root FS writable
	// because the user may have shipped an app that writes to it.
	StaticDrop bool
	// MemoryBytes overrides the runtime default memory limit for this
	// app's containers. 0 means "use the runtime default".
	MemoryBytes int64
	// PidsLimit overrides the runtime default PIDs limit. 0 means "use
	// the runtime default".
	PidsLimit int64
	// Kind is the app kind ("web"/"function"/"custom"), recorded as a
	// container label so a future scale-to-zero activator can discover
	// function containers by label.
	Kind string
	// ScaleToZero marks containers a future activator may stop when idle
	// and start on demand. Inert until the activator lands.
	ScaleToZero bool
}
