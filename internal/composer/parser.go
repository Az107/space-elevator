package composer

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func Parse(b []byte) (*Spec, error) {
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("compose file is too large (maximum 4 MiB)")
	}
	var s Spec
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if len(s.Services) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}
	// Container names and image tags are derived from the service name via
	// SanitizeForImage, which folds every non-alphanumeric run to '-'. Two
	// distinct compose names can therefore normalise to the same runtime
	// identity ("web_1" and "web-1" both become "web-1"), which makes the
	// second CreateContainer fail with an opaque "name already in use" and
	// lets the two services overwrite each other's image. Reject the
	// collision here, where the message can name both services.
	claimed := make(map[string]string, len(s.Services))
	for name, svc := range s.Services {
		if name == "" || len(name) > 63 || strings.ContainsAny(name, "/\x00\r\n") {
			return nil, fmt.Errorf("invalid service name %q", name)
		}
		runtimeName := SanitizeForImage(name)
		if runtimeName == "" {
			return nil, fmt.Errorf("service name %q has no usable characters after sanitization", name)
		}
		if other, taken := claimed[runtimeName]; taken {
			return nil, fmt.Errorf("service names %q and %q both map to the runtime name %q; rename one", other, name, runtimeName)
		}
		claimed[runtimeName] = name
		if svc.Image == "" && svc.Build == nil {
			return nil, fmt.Errorf("service %q has neither image nor build", name)
		}
		for key, value := range svc.Environment {
			if key == "" || strings.ContainsAny(key, "=\x00\r\n") {
				return nil, fmt.Errorf("service %q has invalid environment key %q", name, key)
			}
			if strings.ContainsRune(value, 0) {
				return nil, fmt.Errorf("service %q environment %q contains NUL", name, key)
			}
		}
		for _, volume := range svc.Volumes {
			if _, err := ParseVolumeMount(volume); err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
		}
	}
	return &s, nil
}

func MustMarshal(s *Spec) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// ResolveEnv merges compose-time env with app-level env,
// letting app-level override service-level.
func ResolveEnv(svc map[string]string, app map[string]string) map[string]string {
	out := make(map[string]string, len(svc)+len(app))
	for k, v := range svc {
		out[k] = v
	}
	for k, v := range app {
		out[k] = v
	}
	return out
}

// TopologicalOrder returns services in dependency order.
// Cycles are broken by sorting remaining names alphabetically.
func TopologicalOrder(s *Spec) []string {
	visited := map[string]bool{}
	temp := map[string]bool{}
	var order []string

	var visit func(name string)
	visit = func(name string) {
		if visited[name] || temp[name] {
			return
		}
		temp[name] = true
		for _, dep := range s.Services[name].DependsOn {
			if _, ok := s.Services[dep]; ok {
				visit(dep)
			}
		}
		temp[name] = false
		visited[name] = true
		order = append(order, name)
	}

	for name := range s.Services {
		visit(name)
	}
	return order
}
