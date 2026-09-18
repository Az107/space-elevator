package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The wizard renders all kind/source choices and the function fields.
func TestDeployFormRendersWizard(t *testing.T) {
	s := newDetailTestServer(t)
	r := chi.NewRouter()
	r.Get("/apps/new", s.handleDeployForm)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/new", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="deploy-wizard"`,
		`name="kind" value="web"`,
		`name="kind" value="function"`,
		`name="kind" value="custom"`,
		`name="source" value="git"`,
		`name="source" value="upload"`,
		`name="language"`,
		`name="entrypoint"`,
		`name="tarball"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard missing %q", want)
		}
	}
}

// A function deploy with a traversing entrypoint is rejected before any
// row is created (validation runs ahead of the deployer).
func TestDeploySubmitRejectsBadFunctionEntrypoint(t *testing.T) {
	s := newDetailTestServer(t)
	r := chi.NewRouter()
	r.Post("/apps/new", s.handleDeploySubmit)

	form := "source=git&kind=function&language=python&entrypoint=..%2Fevil.py%3Ahandler&url=https%3A%2F%2Fexample.com%2Frepo.git&name=my-fn"
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/apps/new", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 with error rendered; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Function:") {
		t.Errorf("expected a function validation error, got: %s", w.Body.String())
	}
	if _, err := s.Store.GetAppByName(t.Context(), "my-fn"); err == nil {
		t.Error("no app row should be created for an invalid function")
	}
}

func TestDeploySubmitRejectsUnknownKind(t *testing.T) {
	s := newDetailTestServer(t)
	r := chi.NewRouter()
	r.Post("/apps/new", s.handleDeploySubmit)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/apps/new", strings.NewReader("source=git&kind=robot&url=https%3A%2F%2Fexample.com%2Fr.git"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Unknown app kind") {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}
}

// The dropzone endpoint reports a missing file as JSON, without touching
// the (unset) deployer.
func TestHandleDropMissingFile(t *testing.T) {
	s := newDetailTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/apps/drop", strings.NewReader("--boundary--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	s.handleDrop(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "tarball") {
		t.Errorf("error should mention the tarball field: %s", w.Body.String())
	}
}
