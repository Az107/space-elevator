package deployer

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/albertoruiz/space-elevator/internal/composer"
)

type backupManifest struct {
	AppID     string          `json:"app_id"`
	ReleaseID string          `json:"release_id"`
	CreatedAt time.Time       `json:"created_at"`
	Storage   []backupStorage `json:"storage"`
}

type backupStorage struct {
	Logical string `json:"logical"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref,omitempty"`
	File    string `json:"file,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// backupStorage snapshots the app-owned storage that can be copied safely
// while the app is stopped. Named volumes use Podman's volume exporter; bind
// directories are archived directly. It deliberately never removes or
// mutates live storage.
func (d *Deployer) backupStorage(ctx context.Context, appID, releaseID string, storage map[string]composer.StorageBinding) (string, error) {
	if appID == "" || releaseID == "" || filepath.Base(appID) != appID || filepath.Base(releaseID) != releaseID || strings.ContainsAny(appID+releaseID, "\x00\r\n") {
		return "", fmt.Errorf("invalid backup identity")
	}
	dir := filepath.Join(d.Opts.BackupDir, appID, releaseID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	manifest := backupManifest{AppID: appID, ReleaseID: releaseID, CreatedAt: time.Now().UTC()}
	keys := make([]string, 0, len(storage))
	for key := range storage {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, logical := range keys {
		binding := storage[logical]
		entry := backupStorage{Logical: logical, Kind: binding.Kind, Ref: binding.Ref}
		var output string
		switch binding.Kind {
		case composer.StorageKindVolume:
			if err := d.verifyVolumeOwner(ctx, appID, binding.Ref); err != nil {
				return "", err
			}
			output = filepath.Join(dir, "volume-"+safeBackupName(logical)+".tar")
			if err := d.exportVolume(ctx, binding.Ref, output); err != nil {
				return "", err
			}
		case composer.StorageKindBind:
			if _, statErr := os.Lstat(binding.Ref); os.IsNotExist(statErr) {
				manifest.Storage = append(manifest.Storage, entry)
				continue
			} else if statErr != nil {
				return "", fmt.Errorf("stat bind %q: %w", logical, statErr)
			}
			output = filepath.Join(dir, "bind-"+safeBackupName(logical)+".tar")
			if err := archiveDirectory(binding.Ref, output); err != nil {
				return "", fmt.Errorf("archive bind %q: %w", logical, err)
			}
		default:
			return "", fmt.Errorf("storage %q has unsupported kind %q", logical, binding.Kind)
		}
		entry.File = filepath.Base(output)
		size, digest, err := fileDigest(output)
		if err != nil {
			return "", fmt.Errorf("digest backup %q: %w", logical, err)
		}
		entry.Size, entry.SHA256 = size, digest
		manifest.Storage = append(manifest.Storage, entry)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if info, statErr := os.Lstat(manifestPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to overwrite symlink %q", manifestPath)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
	}
	if err := writeRegularFileAtomic(manifestPath, data, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

func (d *Deployer) verifyVolumeOwner(ctx context.Context, appID, name string) error {
	if d.Client == nil || name == "" {
		return nil
	}
	volume, err := d.Client.InspectVolume(ctx, name)
	if err != nil {
		// A missing volume is handled by the exporter; do not mask its
		// more useful error message here.
		return nil
	}
	owner := volume.Labels[composer.LabelApp]
	if owner == "" {
		return nil // legacy unlabeled volume, retained for compatibility
	}
	app, err := d.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if owner != app.Slug {
		return fmt.Errorf("volume %q is owned by app %q, not %q", name, owner, app.Slug)
	}
	return nil
}

func (d *Deployer) exportVolume(ctx context.Context, name, output string) error {
	// Prefer the API so backups also work with a custom Podman socket. The
	// volume mountpoint is readable by the same rootless user that owns the
	// service; the app is stopped before this is called, making the tar a
	// consistent filesystem snapshot.
	if d.Client != nil {
		inspected, err := d.Client.InspectVolume(ctx, name)
		if err == nil && inspected.Mountpoint != "" {
			if err := archiveDirectory(inspected.Mountpoint, output); err != nil {
				return fmt.Errorf("archive volume %q: %w", name, err)
			}
			return os.Chmod(output, 0o600)
		}
	}
	// Podman's remote client cannot execute `volume export`; use the local
	// client as a fallback for environments where the API does not expose a
	// mountpoint. Put -o before VOLUME for Podman 4.x compatibility.
	cmd := exec.CommandContext(ctx, "podman", "volume", "export", "-o", output, name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("export volume %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return os.Chmod(output, 0o600)
}

func archiveDirectory(root, output string) (err error) {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	if existing, statErr := os.Lstat(output); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to overwrite symlink %q", output)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".space-elevator-backup-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is not supported in managed bind backups", rel)
		}
		hdr, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		hdr.Name = filepath.ToSlash(rel)
		if entry.IsDir() {
			hdr.Name += "/"
		}
		if headerErr := tw.WriteHeader(hdr); headerErr != nil {
			return headerErr
		}
		if entry.Type().IsRegular() {
			in, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			_, copyErr := io.Copy(tw, in)
			closeErr := in.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}); err != nil {
		_ = tw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, output); err != nil {
		return err
	}
	committed = true
	return nil
}

func fileDigest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func safeBackupName(value string) string {
	value = filepath.Base(filepath.Clean(value))
	if value == "." || value == string(filepath.Separator) || value == "" {
		return "storage"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
