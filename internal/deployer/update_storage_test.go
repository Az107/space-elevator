package deployer

import (
	"path/filepath"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

func TestPlanStoragePreservesNamedVolumes(t *testing.T) {
	old := &composer.Spec{Volumes: map[string]composer.Volume{"db": {}}, Services: map[string]composer.Service{
		"db": {Image: "db:16", Volumes: []string{"db:/var/lib/postgresql/data"}},
	}}
	candidate := &composer.Spec{Volumes: map[string]composer.Volume{"db": {}}, Services: map[string]composer.Service{
		"db": {Image: "db:16", Volumes: []string{"db:/var/lib/postgresql/data"}},
	}}
	d := &Deployer{Opts: Options{AppsRoot: t.TempDir()}}
	app := &store.App{ID: "app-id", Slug: "app"}
	existing := []*store.AppStorage{{AppID: app.ID, LogicalName: "db", StorageKind: store.StorageKindVolume, PhysicalRef: "legacy-db"}}
	plan, err := d.planStorage(t.Context(), app, old, "", candidate, existing)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Candidate["db"].Ref; got != "legacy-db" {
		t.Fatalf("candidate volume = %q, want legacy-db", got)
	}
	if got := plan.All["db"].Ref; got != "legacy-db" {
		t.Fatalf("backup volume = %q, want legacy-db", got)
	}
}

func TestPlanStorageRejectsKindChange(t *testing.T) {
	old := &composer.Spec{Services: map[string]composer.Service{"app": {Image: "app", Volumes: []string{"data:/data"}}}}
	candidate := &composer.Spec{Services: map[string]composer.Service{"app": {Image: "app", Volumes: []string{"./data:/data"}}}}
	d := &Deployer{Opts: Options{AppsRoot: t.TempDir()}}
	app := &store.App{ID: "app-id", Slug: "app"}
	existing := []*store.AppStorage{{AppID: app.ID, LogicalName: "data", StorageKind: store.StorageKindVolume, PhysicalRef: "se-vol-old"}}
	if _, err := d.planStorage(t.Context(), app, old, "", candidate, existing); err == nil {
		t.Fatal("storage kind change was accepted")
	}
}

func TestPlanStorageAllowsSharedVolumeAcrossServices(t *testing.T) {
	spec := &composer.Spec{Services: map[string]composer.Service{
		"one": {Image: "app", Volumes: []string{"data:/data"}},
		"two": {Image: "app", Volumes: []string{"data:/other"}},
	}}
	d := &Deployer{Opts: Options{AppsRoot: t.TempDir()}}
	app := &store.App{ID: "app-id", Slug: "app"}
	plan, err := d.planStorage(t.Context(), app, spec, "", spec, nil)
	if err != nil || len(plan.Candidate) != 1 {
		t.Fatalf("shared volume plan = %#v, err=%v", plan, err)
	}
}

func TestPlanStorageMigratesLegacyWritableBind(t *testing.T) {
	oldSource := t.TempDir()
	old := &composer.Spec{Services: map[string]composer.Service{
		"app": {Image: "app:old", Volumes: []string{"./data:/data"}},
	}}
	candidate := &composer.Spec{Services: map[string]composer.Service{
		"app": {Image: "app:new", Volumes: []string{"./data:/data"}},
	}}
	d := &Deployer{Opts: Options{AppsRoot: t.TempDir()}}
	app := &store.App{ID: "app-id", Slug: "app"}
	plan, err := d.planStorage(t.Context(), app, old, oldSource, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	managed := plan.Candidate["data"].Ref
	if managed == filepath.Join(oldSource, "data") {
		t.Fatal("candidate bind must not remain inside the replaced source directory")
	}
	if plan.MigrateBinds[filepath.Join(oldSource, "data")] != managed {
		t.Fatalf("migration = %#v, want old source -> %s", plan.MigrateBinds, managed)
	}
	if plan.All["data"].Ref != filepath.Join(oldSource, "data") {
		t.Fatalf("backup bind = %q, want legacy source", plan.All["data"].Ref)
	}
}
