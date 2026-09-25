package composer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveBuildContext validates a compose build context and returns its
// canonical absolute path. Build contexts are repository-controlled input;
// accepting ../ components or a symlinked parent would expose host files to
// the image builder.
func resolveBuildContext(sourceDir, contextDir string) (string, error) {
	if info, err := os.Lstat(sourceDir); err != nil {
		return "", err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("app source directory is a symlink")
	}
	contextDir = strings.TrimSpace(contextDir)
	if contextDir == "" {
		contextDir = "."
	}
	if filepath.IsAbs(contextDir) {
		return "", fmt.Errorf("build context %q must be relative to the app source", contextDir)
	}
	clean := filepath.Clean(filepath.FromSlash(contextDir))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("build context %q escapes the app source", contextDir)
	}
	return containedPath(sourceDir, filepath.Join(sourceDir, clean))
}

// containedPath verifies both the lexical and symlink-resolved location of
// candidate under root. Missing leaf components are allowed; the nearest
// existing parent is resolved before the remaining components are appended.
func containedPath(root, candidate string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve source root: %w", err)
		}
		// Keep pure path-validation helpers usable before a checkout is
		// created (notably unit tests); real builds always have a source root.
		rootReal = rootAbs
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes source root", candidate)
	}

	realCandidate, err := filepath.EvalSymlinks(candidateAbs)
	if err == nil {
		if err := ensureUnder(rootReal, realCandidate); err != nil {
			return "", err
		}
		return candidateAbs, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve path %q: %w", candidate, err)
	}

	// Resolve the nearest existing parent so a missing final component cannot
	// hide a symlinked parent directory.
	parent := filepath.Dir(candidateAbs)
	var suffix []string
	for {
		realParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr == nil {
			parts := append([]string{realParent}, reversePathParts(suffix)...)
			resolved := filepath.Join(parts...)
			if err := ensureUnder(rootReal, resolved); err != nil {
				return "", err
			}
			return candidateAbs, nil
		}
		if !os.IsNotExist(parentErr) {
			return "", fmt.Errorf("resolve path %q: %w", candidate, parentErr)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("resolve path %q: no existing parent", candidate)
		}
		suffix = append(suffix, filepath.Base(parent))
		parent = next
	}
}

func ensureUnder(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes source root", candidate)
	}
	return nil
}

func reversePathParts(parts []string) []string {
	out := make([]string, len(parts))
	for i := range parts {
		out[len(parts)-1-i] = parts[i]
	}
	return out
}
