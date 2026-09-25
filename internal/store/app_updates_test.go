package store

import (
	"errors"
	"testing"
	"time"
)

func mustUpdateApp(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.CreateApp(t.Context(), &App{ID: id, Name: id, SourceType: "git", Status: "running", Env: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
}

func TestAppStorageUpsertAndList(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "storage-app")

	first := &AppStorage{AppID: "storage-app", LogicalName: "data", StorageKind: StorageKindVolume, PhysicalRef: "app-data"}
	if err := s.UpsertAppStorage(ctx, first); err != nil {
		t.Fatal(err)
	}
	created := first.CreatedAt
	if err := s.UpsertAppStorage(ctx, &AppStorage{
		AppID: "storage-app", LogicalName: "data", StorageKind: StorageKindBind, PhysicalRef: "/srv/data",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAppStorage(ctx, &AppStorage{
		AppID: "storage-app", LogicalName: "uploads", StorageKind: StorageKindVolume, PhysicalRef: "uploads",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListAppStorage(ctx, "storage-app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d mappings, want 2", len(got))
	}
	if got[0].LogicalName != "data" || got[0].StorageKind != StorageKindBind || got[0].PhysicalRef != "/srv/data" {
		t.Errorf("updated mapping = %+v", got[0])
	}
	if !got[0].CreatedAt.Equal(created.Truncate(time.Second)) {
		t.Errorf("created timestamp changed on upsert: %v -> %v", created, got[0].CreatedAt)
	}
}

func TestAppReleaseRoundTripAndCurrent(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "release-app")

	release := &AppRelease{
		ID: "release-1", AppID: "release-app", SourceType: "git", SourceRef: "https://example.test/repo.git",
		GitRef: "main", GitCommit: "abc123", SourcePath: "/srv/source/release-1", ComposeYAML: "services: {}\n",
		Kind: KindWeb, BuildMode: BuildModeStatic, ServePath: "dist", ImageMap: map[string]string{"web": "registry.example/web:1"},
		Status: "pending",
	}
	if err := s.CreateRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GitCommit != "abc123" || got.ImageMap["web"] != "registry.example/web:1" || got.ServePath != "dist" {
		t.Fatalf("release did not round trip: %+v", got)
	}
	if err := s.SetCurrentRelease(ctx, "release-app", release.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetCurrentRelease(ctx, "release-app")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != release.ID {
		t.Errorf("current release = %q, want %q", current.ID, release.ID)
	}
	app, err := s.GetApp(ctx, "release-app")
	if err != nil {
		t.Fatal(err)
	}
	if app.CurrentReleaseID != release.ID {
		t.Errorf("app current release = %q, want %q", app.CurrentReleaseID, release.ID)
	}
	app.SourceRef = "https://example.test/next.git"
	app.CurrentReleaseID = ""
	if err := s.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	updatedApp, err := s.GetApp(ctx, "release-app")
	if err != nil {
		t.Fatal(err)
	}
	if updatedApp.SourceRef != app.SourceRef || updatedApp.CurrentReleaseID != "" {
		t.Errorf("UpdateApp current release round trip = %+v", updatedApp)
	}

	release.Status = "running"
	release.ImageMapJSON = `{"web":"registry.example/web:2"}`
	release.ImageMap = nil
	if err := s.UpdateRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "running" || updated.ImageMap["web"] != "registry.example/web:2" {
		t.Errorf("updated release = %+v", updated)
	}
	list, err := s.ListReleases(ctx, "release-app")
	if err != nil || len(list) != 1 || list[0].ID != release.ID {
		t.Fatalf("release list = %+v, err = %v", list, err)
	}
}

func TestClaimOperationConflictAndLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "operation-app")

	first := &AppOperation{ID: "operation-1", AppID: "operation-app", OperationType: "update"}
	if err := s.ClaimOperation(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.Status != OperationStatusPreflighting || first.HeartbeatAt.IsZero() {
		t.Fatalf("claimed operation = %+v", first)
	}
	second := &AppOperation{ID: "operation-2", AppID: "operation-app", OperationType: "update"}
	if err := s.ClaimOperation(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim error = %v, want ErrConflict", err)
	}
	if err := s.HeartbeatOperation(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperation(ctx, first.ID, OperationStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperation(ctx, first.ID, OperationStatusFailed, "late worker"); err != nil {
		t.Fatalf("idempotent finalization: %v", err)
	}
	finished, err := s.GetOperation(ctx, first.ID)
	if err != nil || finished.Status != OperationStatusCompleted {
		t.Fatalf("late finalization changed terminal state: %+v, %v", finished, err)
	}
	if err := s.ClaimOperation(ctx, second); err != nil {
		t.Fatalf("claim after finish failed: %v", err)
	}
	claimed, err := s.GetOperation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != OperationStatusPreflighting {
		t.Errorf("status = %q, want %q", claimed.Status, OperationStatusPreflighting)
	}
}

func TestDeployStepsAndLogsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "progress-app")
	op, err := s.ClaimAppOperation(ctx, "progress-app", "create")
	if err != nil {
		t.Fatal(err)
	}
	steps := []DeployStep{
		{Key: "validate", Label: "Validate request"},
		{Key: "build", Label: "Build images"},
	}
	if err := s.CreateDeploySteps(ctx, op.ID, steps); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationStep(ctx, op.ID, "validate"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDeployStep(ctx, op.ID, "validate", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationStep(ctx, op.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendDeployLog(ctx, op.ID, "build", "info", "pulling base image"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendDeployLog(ctx, op.ID, "build", "error", "build failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDeployStep(ctx, op.ID, "build", "failed", "build failed"); err != nil {
		t.Fatal(err)
	}

	gotSteps, err := s.ListDeploySteps(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSteps) != 2 || gotSteps[0].Status != "succeeded" || gotSteps[1].Status != "failed" {
		t.Fatalf("steps = %+v", gotSteps)
	}
	logs, latest, err := s.ListDeployLogs(ctx, op.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if latest != 2 || len(logs) != 1 || logs[0].Message != "build failed" {
		t.Fatalf("logs = %+v, latest = %d", logs, latest)
	}
	operation, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.CurrentStep != "build" || operation.Attempt != 1 {
		t.Fatalf("operation = %+v", operation)
	}
}

func TestListDeployLogsLargeFeedDoesNotDeadlock(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "large-log-app")
	op, err := s.ClaimAppOperation(ctx, "large-log-app", "create")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if _, err := s.AppendDeployLog(ctx, op.ID, "build", "info", "line"); err != nil {
			t.Fatal(err)
		}
	}
	logs, latest, err := s.ListDeployLogs(ctx, op.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 500 || latest != 501 {
		t.Fatalf("logs=%d latest=%d, want 500/501", len(logs), latest)
	}
}

func TestInterruptActiveOperations(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "interrupted-app")
	op, err := s.ClaimAppOperation(ctx, "interrupted-app", "create")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InterruptActiveOperations(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != OperationStatusInterrupted {
		t.Fatalf("status = %q, want %q", got.Status, OperationStatusInterrupted)
	}
	app, err := s.GetApp(ctx, "interrupted-app")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "error" || app.LastError == "" {
		t.Fatalf("app = %+v", app)
	}
}

func TestUpdateAppCurrentRelease(t *testing.T) {
	// Keep this test explicit about the nullable app column without
	// depending on a release: an app starts with no current release.
	s := openTestStore(t)
	ctx := t.Context()
	mustUpdateApp(t, s, "empty-release-app")
	app, err := s.GetApp(ctx, "empty-release-app")
	if err != nil {
		t.Fatal(err)
	}
	if app.CurrentReleaseID != "" {
		t.Errorf("initial current release = %q, want empty", app.CurrentReleaseID)
	}
}
