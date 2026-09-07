package store

import (
	"fmt"
	"path/filepath"
)

// AppSourceDir returns the on-disk path where the source for an app lives.
// Git clones live under <root>/sources/<id>; tarball drops under
// <root>/drops/<id>. Returns an error for unknown source types so callers
// can refuse to render a viewer for them.
func AppSourceDir(appsRoot string, a *App) (string, error) {
	switch a.SourceType {
	case "git":
		return filepath.Join(appsRoot, "sources", a.ID), nil
	case "drop":
		return filepath.Join(appsRoot, "drops", a.ID), nil
	default:
		return "", fmt.Errorf("unknown source type %q", a.SourceType)
	}
}
