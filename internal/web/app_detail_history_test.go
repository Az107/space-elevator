package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/go-chi/chi/v5"
)

func TestDeploymentHistoryRendersAfterDetailPanels(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "history-app", Name: "history-app", SourceType: "git", Status: "pending", Env: map[string]string{}})
	if _, err := s.Store.ClaimAppOperation(t.Context(), "history-app", "create"); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Get("/apps/{name}", s.handleAppDetail)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/history-app", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	history := strings.Index(w.Body.String(), "Deployment history")
	logs := strings.Index(w.Body.String(), "id=\"logs-panel\"")
	if history < 0 || logs < 0 || history < logs {
		t.Fatalf("deployment history position is wrong: history=%d logs=%d", history, logs)
	}
}
