package deployer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type storagePlan struct {
	// Candidate contains every logical source used by the candidate spec.
	Candidate map[string]composer.StorageBinding
	// All contains candidate storage plus retained storage removed by the
	// candidate, so backups still cover data that is no longer mounted.
	All          map[string]composer.StorageBinding
	Previous     map[string]composer.StorageBinding
	MigrateBinds map[string]string
	Warnings     []string
}

func (d *Deployer) planStorage(ctx context.Context, a *store.App, oldSpec *composer.Spec, oldSourceDir string, candidate *composer.Spec, existing []*store.AppStorage) (*storagePlan, error) {
	plan := &storagePlan{
		Candidate:    map[string]composer.StorageBinding{},
		All:          map[string]composer.StorageBinding{},
		Previous:     map[string]composer.StorageBinding{},
		MigrateBinds: map[string]string{},
	}
	known := make(map[string]store.AppStorage, len(existing))
	for _, mapping := range existing {
		if mapping == nil {
			continue
		}
		known[mapping.LogicalName] = *mapping
		binding := composer.StorageBinding{Kind: mapping.StorageKind, Ref: mapping.PhysicalRef}
		plan.All[mapping.LogicalName] = binding
		plan.Previous[mapping.LogicalName] = binding
	}

	discovered := d.discoverStorage(ctx, a, oldSpec)
	legacy := map[string]composer.StorageBinding{}
	declaredKinds := map[string]string{}
	for logical, discoveredBinding := range discovered {
		if _, ok := known[logical]; ok {
			continue
		}
		legacy[logical] = discoveredBinding
		// Keep the legacy physical location in All for the backup, even
		// when a writable bind will move to managed storage below.
		plan.All[logical] = discoveredBinding
		plan.Previous[logical] = discoveredBinding
	}

	for _, service := range candidate.Services {
		for _, raw := range service.Volumes {
			mount, err := composer.ParseVolumeMount(raw)
			if err != nil {
				return nil, err
			}
			logical, kind, err := logicalStorageName(mount.Source)
			if err != nil {
				return nil, fmt.Errorf("service %q volume %q: %w", serviceName(candidate, raw), raw, err)
			}
			if previousKind, exists := declaredKinds[logical]; exists {
				if previousKind != kind {
					return nil, fmt.Errorf("storage %q is declared as both %s and %s", logical, previousKind, kind)
				}
				// Multiple services may intentionally share one volume.
				continue
			}
			declaredKinds[logical] = kind
			binding, ok := known[logical]
			if ok && binding.StorageKind != kind {
				return nil, fmt.Errorf("storage %q changed kind from %s to %s; remove the old mapping explicitly before deploying", logical, binding.StorageKind, kind)
			}
			if !ok {
				if kind == composer.StorageKindVolume {
					ref := d.volumeName(a.ID, logical)
					if previous, found := legacy[logical]; found && previous.Kind == composer.StorageKindVolume {
						ref = previous.Ref
					}
					binding = store.AppStorage{AppID: a.ID, LogicalName: logical, StorageKind: kind, PhysicalRef: ref}
				} else {
					clean, cleanErr := cleanRelativeStorage(logical)
					if cleanErr != nil {
						return nil, cleanErr
					}
					binding = store.AppStorage{AppID: a.ID, LogicalName: logical, StorageKind: kind, PhysicalRef: filepath.Join(d.Opts.AppsRoot, "data", a.ID, filepath.FromSlash(clean))}
				}
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("persistent storage %q -> %s", logical, binding.PhysicalRef))
			}
			candidateBinding := composer.StorageBinding{Kind: binding.StorageKind, Ref: binding.PhysicalRef}
			plan.Candidate[logical] = candidateBinding
			plan.All[logical] = candidateBinding
			if kind == composer.StorageKindBind && !mount.ReadOnly {
				if oldSource := d.oldBindSource(discovered, logical, oldSourceDir); oldSource != "" && oldSource != binding.PhysicalRef {
					plan.MigrateBinds[oldSource] = binding.PhysicalRef
					if previous, found := legacy[logical]; found {
						plan.All[logical] = previous
					} else {
						plan.All[logical] = composer.StorageBinding{Kind: composer.StorageKindBind, Ref: oldSource}
					}
				}
			}
		}
	}
	if len(plan.MigrateBinds) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d writable bind path(s) will be copied into managed storage", len(plan.MigrateBinds)))
	}
	return plan, nil
}

func serviceName(spec *composer.Spec, raw string) string {
	for name, service := range spec.Services {
		for _, mount := range service.Volumes {
			if mount == raw {
				return name
			}
		}
	}
	return "unknown"
}

func logicalStorageName(source string) (string, string, error) {
	if source == "" || strings.HasPrefix(source, "/") {
		return "", "", fmt.Errorf("absolute host paths are not allowed")
	}
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return composer.CanonicalStorageKey(source), composer.StorageKindBind, nil
	}
	return composer.CanonicalStorageKey(source), composer.StorageKindVolume, nil
}

func cleanRelativeStorage(source string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(source))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("relative storage path %q escapes app data", source)
	}
	return filepath.ToSlash(clean), nil
}

func (d *Deployer) volumeName(appID, logical string) string {
	return composer.ManagedVolumeName(appID, logical)
}

func (d *Deployer) discoverStorage(ctx context.Context, a *store.App, spec *composer.Spec) map[string]composer.StorageBinding {
	out := map[string]composer.StorageBinding{}
	if spec == nil || d.Client == nil {
		return out
	}
	containers, err := d.Client.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {composer.LabelApp + "=" + a.Slug},
	})
	if err != nil {
		return out
	}
	for _, ctr := range containers {
		mounts, err := d.Client.ContainerMounts(ctx, ctr.ID)
		if err != nil {
			continue
		}
		service := ""
		for candidateName := range spec.Services {
			if ctr.Name == "se-"+sanitizeAppName(a.Slug)+"-"+sanitizeAppName(candidateName) {
				service = candidateName
				break
			}
		}
		if service == "" {
			continue
		}
		svc := spec.Services[service]
		for _, raw := range svc.Volumes {
			mount, err := composer.ParseVolumeMount(raw)
			if err != nil {
				continue
			}
			logical, kind, err := logicalStorageName(mount.Source)
			if err != nil {
				continue
			}
			for _, actual := range mounts {
				if actual.Destination != mount.Target {
					continue
				}
				if kind == composer.StorageKindVolume && actual.Type == "volume" {
					ref := actual.Name
					if ref == "" {
						ref = actual.Source
					}
					if ref != "" && d.Client != nil {
						if volume, inspectErr := d.Client.InspectVolume(ctx, ref); inspectErr == nil {
							if owner := volume.Labels[composer.LabelApp]; owner != "" && owner != a.Slug {
								continue
							}
						}
					}
					if ref == "" {
						continue
					}
					out[logical] = composer.StorageBinding{Kind: kind, Ref: ref}
				} else if kind == composer.StorageKindBind && actual.Type == "bind" {
					out[logical] = composer.StorageBinding{Kind: kind, Ref: actual.Source}
				}
			}
		}
	}
	// A stopped/partially-created app may have no container inspect. Compose's
	// named volume name is still the legacy physical name and is safe to retain.
	for _, svc := range spec.Services {
		for _, raw := range svc.Volumes {
			mount, err := composer.ParseVolumeMount(raw)
			if err != nil {
				continue
			}
			logical, kind, err := logicalStorageName(mount.Source)
			if err != nil || kind != composer.StorageKindVolume {
				continue
			}
			if _, ok := out[logical]; !ok {
				out[logical] = composer.StorageBinding{Kind: kind, Ref: logical}
			}
		}
	}
	return out
}

func (d *Deployer) oldBindSource(discovered map[string]composer.StorageBinding, logical, oldSourceDir string) string {
	if oldSourceDir == "" {
		return ""
	}
	ref := discovered[logical].Ref
	if ref != "" {
		return ref
	}
	// Relative mounts are the only supported host-bind form. The old runtime
	// resolved them beneath the active checkout.
	return filepath.Join(oldSourceDir, filepath.FromSlash(strings.TrimPrefix(logical, "./")))
}

func sanitizeAppName(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func writeRegularFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".space-elevator-migrate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func copyDirectory(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if existing, err := os.Lstat(dst); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration destination is a symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return filepath.Walk(src, func(path string, entry os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("destination contains symlink %q", target)
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return statErr
			}
			return os.MkdirAll(target, 0o700)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q cannot be migrated", rel)
		}
		if !entry.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination contains symlink %q", target)
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		return writeRegularFileAtomic(target, data, entry.Mode().Perm())
	})
}
