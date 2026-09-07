package traefik

import (
	"fmt"
	"os"
	"path/filepath"
)

// SelfRouteFileName is the Traefik dynamic file that exposes the dashboard.
// It is a fixed name so it can be added or removed atomically without
// conflicting with per-app files (<appname>.yml).
const SelfRouteFileName = "space-elevator.yml"

type Writer struct {
	Dir          string
	CertResolver string
}

func NewWriter(dir, certResolver string) *Writer {
	return &Writer{Dir: dir, CertResolver: certResolver}
}

func (w *Writer) path(appName string) string {
	return filepath.Join(w.Dir, sanitize(appName)+".yml")
}

// Write renders and atomically replaces the per-app dynamic file. Empty
// rendered output removes the file instead of writing an empty YAML.
func (w *Writer) Write(appName string, cfg AppRouteConfig) error {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return err
	}
	if w.CertResolver != "" && cfg.CertResolver == "" {
		cfg.CertResolver = w.CertResolver
	}
	body, err := Render(cfg)
	if err != nil {
		return err
	}
	p := w.path(appName)
	if len(body) == 0 {
		return w.Remove(appName)
	}
	return os.WriteFile(p, body, 0o644)
}

// Remove deletes the per-app dynamic file. Missing file is not an error.
func (w *Writer) Remove(appName string) error {
	err := os.Remove(w.path(appName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (w *Writer) Read(appName string) ([]byte, error) {
	b, err := os.ReadFile(w.path(appName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", w.path(appName), err)
	}
	return b, nil
}

// WriteSelf writes the dashboard's Traefik dynamic file.
func (w *Writer) WriteSelf(c SelfRouteConfig) error {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return err
	}
	body, err := RenderSelf(c)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return w.RemoveSelf()
	}
	p := filepath.Join(w.Dir, SelfRouteFileName)
	return os.WriteFile(p, body, 0o644)
}

// RemoveSelf deletes the dashboard's Traefik dynamic file. Missing file is
// not an error.
func (w *Writer) RemoveSelf() error {
	p := filepath.Join(w.Dir, SelfRouteFileName)
	err := os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SelfExists reports whether the dashboard dynamic file is currently present.
func (w *Writer) SelfExists() (bool, error) {
	p := filepath.Join(w.Dir, SelfRouteFileName)
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
