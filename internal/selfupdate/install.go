package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Installer replaces one binary atomically.
type Installer struct {
	// Target is the absolute path of the binary to replace.
	Target string
	// Log receives progress lines; may be nil.
	Log func(format string, a ...any)
}

func (in *Installer) logf(format string, a ...any) {
	if in.Log != nil {
		in.Log(format, a...)
	}
}

// TargetFromUnit reads the ExecStart binary path from a systemd unit file.
// This is the authoritative install target: the unit, not the CLI process,
// decides which binary gets executed after the restart.
func TargetFromUnit(unitPath string) (string, error) {
	b, err := os.ReadFile(unitPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		val = strings.TrimPrefix(val, "-") // systemd "-" prefix: ignore failure
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		p := strings.Trim(fields[0], `"'`)
		p = strings.TrimPrefix(p, "-") // "-/path" after unquoting
		if p != "" {
			return expandSpecifiers(p), nil
		}
	}
	return "", fmt.Errorf("no ExecStart binary found in %s", unitPath)
}

// expandSpecifiers resolves the systemd specifiers that appear in hand-written
// or legacy units. The current template writes an absolute path, but older
// units on this host use %h.
func expandSpecifiers(p string) string {
	if !strings.Contains(p, "%") {
		return p
	}
	home, _ := os.UserHomeDir()
	p = strings.ReplaceAll(p, "%%", "\x00")
	p = strings.ReplaceAll(p, "%h", home)
	p = strings.ReplaceAll(p, "%u", os.Getenv("USER"))
	return strings.ReplaceAll(p, "\x00", "%")
}

// Preflight refuses obviously unsafe targets before anything is downloaded.
func (in *Installer) Preflight() error {
	if in.Target == "" {
		return errors.New("no install target")
	}
	if !filepath.IsAbs(in.Target) {
		return fmt.Errorf("install target must be an absolute path: %s", in.Target)
	}
	info, err := os.Lstat(in.Target)
	if err != nil {
		return fmt.Errorf("install target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlinked binary %s", in.Target)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("install target is not a regular file: %s", in.Target)
	}
	if err := writableDir(filepath.Dir(in.Target)); err != nil {
		return err
	}
	return nil
}

// Stage writes r to a private temporary file in the target's directory and
// returns its path and sha256. The target itself is never opened for writing:
// on Linux a running executable cannot be opened for write (ETXTBSY); only
// an unlink+rename can replace it.
func (in *Installer) Stage(r io.Reader) (tmpPath, sha string, err error) {
	dir := filepath.Dir(in.Target)
	f, err := os.CreateTemp(dir, ".space-elevator-update-*")
	if err != nil {
		return "", "", err
	}
	tmpPath = f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmpPath) }

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		cleanup()
		return "", "", err
	}
	if err := f.Chmod(0o755); err != nil {
		cleanup()
		return "", "", err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", err
	}
	return tmpPath, hex.EncodeToString(h.Sum(nil)), nil
}

// Commit backs up Target to Target.prev and renames the staged file over it.
// The rename is atomic and works while the current binary is still running.
func (in *Installer) Commit(tmpPath, expectedSHA string) error {
	prev := in.Target + ".prev"
	if err := copyFile(in.Target, prev); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}
	if err := os.Rename(tmpPath, in.Target); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	got, err := FileSHA(in.Target)
	if err != nil {
		return err
	}
	if expectedSHA != "" && got != expectedSHA {
		// The bytes were hashed before the rename, so a mismatch here means
		// the install itself went wrong. Put the previous binary back rather
		// than leaving a corrupt target in place.
		if rerr := in.Restore(); rerr == nil {
			return fmt.Errorf("installed sha256 %s does not match verified %s; previous binary restored", got, expectedSHA)
		}
		return fmt.Errorf("installed sha256 %s does not match verified %s", got, expectedSHA)
	}
	in.logf("previous binary saved: %s", prev)
	return nil
}

// Restore copies Target.prev back over Target, undoing the last Commit.
func (in *Installer) Restore() error {
	prev := in.Target + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("no previous binary to restore (%s): %w", prev, err)
	}
	dir := filepath.Dir(in.Target)
	tmp, err := os.CreateTemp(dir, ".space-elevator-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	if err := copyFile(prev, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, in.Target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// HasPrevious reports whether a rollback target exists.
func (in *Installer) HasPrevious() bool {
	_, err := os.Stat(in.Target + ".prev")
	return err == nil
}

// FileSHA returns the sha256 of a file.
func FileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".space-elevator-probe-*")
	if err != nil {
		return fmt.Errorf("install directory %s is not writable: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}
