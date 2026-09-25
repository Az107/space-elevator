package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/store"
)

func TestDeploymentTextIsBoundedForUI(t *testing.T) {
	huge := strings.Repeat("x", 10_000)
	if got := boundedDeploymentError(huge); len(got) > 2100 || !strings.HasSuffix(got, "…") {
		t.Fatalf("error length = %d", len(got))
	}
	if got := boundedDeploymentLog(huge); len(got) > 4200 || !strings.HasSuffix(got, "…") {
		t.Fatalf("log length = %d", len(got))
	}
}

func TestDeploymentPageRendersDurablePlanAndLogs(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "deployment-app", Name: "deployment-app", SourceType: "git", Status: "pending", Env: map[string]string{}})
	op, err := s.Store.ClaimAppOperation(t.Context(), "deployment-app", "create")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.CreateDeploySteps(t.Context(), op.ID, []store.DeployStep{{Key: "build", Label: "Build images", Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.AppendDeployLog(t.Context(), op.ID, "build", "info", "pulling image"); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Get("/deployments/{id}", s.handleDeploymentDetail)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/deployments/"+op.ID, nil))
	if w.Code != 200 {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"Deployment plan", "Build images", "pulling image", `id="deployment-steps"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("deployment page missing %q", want)
		}
	}
}

func TestDeploymentStatusReturnsStepsAndIncrementalLogs(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "deployment-app-2", Name: "deployment-app-2", SourceType: "git", Status: "error", Env: map[string]string{}})
	op, err := s.Store.ClaimAppOperation(t.Context(), "deployment-app-2", "create")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Store.FinishOperation(t.Context(), op.ID, store.OperationStatusFailed, "build failed")
	if err := s.Store.CreateDeploySteps(t.Context(), op.ID, []store.DeployStep{{Key: "build", Label: "Build images", Status: "failed", Error: "build failed"}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Store.AppendDeployLog(t.Context(), op.ID, "build", "info", "first")
	_, _ = s.Store.AppendDeployLog(t.Context(), op.ID, "build", "error", "second")

	r := chi.NewRouter()
	r.Get("/deployments/{id}/status", s.handleDeploymentStatus)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/deployments/"+op.ID+"/status?after=1", nil))
	if w.Code != 200 {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}
	var got deploymentStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != store.OperationStatusFailed || got.Seq != 2 || len(got.Steps) != 1 || len(got.Lines) != 1 || got.Lines[0].Message != "second" {
		t.Fatalf("status = %+v", got)
	}
}
