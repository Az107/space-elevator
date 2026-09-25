package deployer

import (
	"fmt"
	"path/filepath"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

func releaseDir(appsRoot, appID, releaseID string) string {
	return filepath.Join(appsRoot, "releases", appID, releaseID)
}

func releaseMeta(a *store.App, releaseID string, spec *composer.Spec, storage map[string]composer.StorageBinding, runtime *composer.Runtime) composer.AppMeta {
	imageTags := make(map[string]string, len(spec.Services))
	for service := range spec.Services {
		imageTags[service] = runtime.ReleaseImageTag(a.Slug, service, releaseID)
	}
	return composer.AppMeta{
		ID: a.ID, Name: a.Slug, Label: a.Slug, Storage: storage, ImageTags: imageTags,
		Kind: a.Kind, ScaleToZero: a.ScaleToZero,
	}
}

func validateReleaseID(id string) error {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return fmt.Errorf("invalid release id %q", id)
	}
	return nil
}
