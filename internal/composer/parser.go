package composer

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

func Parse(b []byte) (*Spec, error) {
	var s Spec
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if len(s.Services) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}
	for name, svc := range s.Services {
		if svc.Image == "" && svc.Build == nil {
			return nil, fmt.Errorf("service %q has neither image nor build", name)
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