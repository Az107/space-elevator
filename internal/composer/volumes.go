package composer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	StorageKindVolume = "volume"
	StorageKindBind   = "bind"
)

// ManagedVolumeName returns a stable, app-scoped physical volume name.
// Compose logical names are not globally unique and must never be used as
// the physical Podman name for a new app.
func ManagedVolumeName(appID, logical string) string {
	sum := sha256.Sum256([]byte(logical))
	return "se-vol-" + appID + "-" + hex.EncodeToString(sum[:])[:12]
}

// StorageBinding is the physical location behind a logical compose volume
// source. Keeping this translation in the runtime makes app identity and
// persistence independent from source-directory replacement.
type StorageBinding struct {
	Kind string
	Ref  string
}

// VolumeMount is the short compose syntax supported by the custom runtime.
type VolumeMount struct {
	Source   string
	Target   string
	Mode     string
	ReadOnly bool
}

func ParseVolumeMount(value string) (VolumeMount, error) {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return VolumeMount{}, fmt.Errorf("invalid volume %q", value)
	}
	m := VolumeMount{
		Source: parts[0],
		Target: parts[1],
		Mode:   "rw",
	}
	if m.Source == "" || !strings.HasPrefix(m.Target, "/") {
		return VolumeMount{}, fmt.Errorf("invalid volume %q", value)
	}
	if len(parts) == 3 {
		m.Mode = parts[2]
	}
	for _, opt := range strings.Split(m.Mode, ",") {
		switch strings.TrimSpace(opt) {
		case "", "rw":
		case "ro":
			m.ReadOnly = true
		default:
			// Preserve the historical runtime behavior for options Podman
			// understands but this parser does not interpret (z, Z, etc.).
		}
	}
	return m, nil
}

// CanonicalStorageKey returns the stable logical key used by both the
// storage planner and the runtime bind resolver. In particular, "./data"
// and "data" intentionally share a key; a named volume and a relative bind
// with the same spelling are rejected/handled by the planner rather than
// silently selecting different physical storage.
func CanonicalStorageKey(source string) string {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return filepath.ToSlash(filepath.Clean(source))
	}
	return source
}

// managedBindPath returns a clean logical key for a relative compose bind.
// Absolute paths and traversal are rejected by resolveBindsWithStorage.
func managedBindPath(source string) (string, error) {
	clean := filepath.Clean(source)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || clean == "" || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative bind %q", source)
	}
	return filepath.ToSlash(clean), nil
}
