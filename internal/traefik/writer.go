package traefik

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if w.Dir == "" {
		return nil
	}
	if sanitize(appName) == "" {
		return fmt.Errorf("app name is empty after sanitization")
	}
	if sanitize(appName) == strings.TrimSuffix(SelfRouteFileName, ".yml") {
		return fmt.Errorf("app name %q is reserved for the dashboard route", appName)
	}
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
	return atomicWriteFile(p, body, 0o644)
}

// Remove deletes the per-app dynamic file. Missing file is not an error.
func (w *Writer) Remove(appName string) error {
	if w.Dir == "" {
		return nil
	}
	if sanitize(appName) == "" {
		return fmt.Errorf("app name is empty after sanitization")
	}
	if sanitize(appName) == strings.TrimSuffix(SelfRouteFileName, ".yml") {
		return fmt.Errorf("refusing to remove reserved dashboard route")
	}
	err := os.Remove(w.path(appName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (w *Writer) Read(appName string) ([]byte, error) {
	if sanitize(appName) == "" {
		return nil, fmt.Errorf("app name is empty after sanitization")
	}
	if w.Dir == "" {
		return nil, fmt.Errorf("Traefik directory is not configured")
	}
	b, err := os.ReadFile(w.path(appName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", w.path(appName), err)
	}
	return b, nil
}

func (w *Writer) ReadSelf() ([]byte, error) {
	if w.Dir == "" {
		return nil, fmt.Errorf("Traefik directory is not configured")
	}
	path := filepath.Join(w.Dir, SelfRouteFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// WriteSelf writes the dashboard's Traefik dynamic file.
func (w *Writer) WriteSelf(c SelfRouteConfig) error {
	if w.Dir == "" {
		return fmt.Errorf("Traefik directory is not configured")
	}
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
	return atomicWriteFile(filepath.Join(w.Dir, SelfRouteFileName), body, 0o644)
}

// RemoveSelf deletes the dashboard's Traefik dynamic file. Missing file is
// not an error.
func (w *Writer) RemoveSelf() error {
	if w.Dir == "" {
		return nil
	}
	p := filepath.Join(w.Dir, SelfRouteFileName)
	err := os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SelfExists reports whether the dashboard dynamic file is currently present.
func (w *Writer) SelfExists() (bool, error) {
	if w.Dir == "" {
		return false, nil
	}
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

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".space-elevator-traefik-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
