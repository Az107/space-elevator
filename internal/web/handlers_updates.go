package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type appUpdateData struct {
	PageData
	App        *store.App
	SourceType string
	Error      string
}

func (s *Server) handleAppUpdateForm(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	s.Renderer.Render(w, r, "update.html", appUpdateData{
		PageData: pageCtx(r, "Update "+a.Name), App: a, SourceType: a.SourceType,
	})
}

func (s *Server) handleAppUpdateSubmit(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.renderUpdateError(w, r, a, "Could not parse update form: "+err.Error())
		return
	}
	req := deployer.UpdateRequest{Name: a.Name, Ref: strings.TrimSpace(r.FormValue("ref"))}
	if a.SourceType == "drop" {
		archive, _, archiveErr := updateArchiveFromRequest(r)
		if archiveErr != nil {
			s.renderUpdateError(w, r, a, archiveErr.Error())
			return
		}
		req.ArchivePath = archive
		req.SourceRef = strings.TrimSpace(r.FormValue("source_name"))
	}
	operation, claimErr := s.Store.ClaimAppOperation(r.Context(), a.ID, "update")
	if claimErr != nil {
		if req.ArchivePath != "" {
			_ = os.Remove(req.ArchivePath)
		}
		if errors.Is(claimErr, store.ErrConflict) {
			s.renderUpdateError(w, r, a, "another update or deploy is already in progress")
		} else {
			s.renderUpdateError(w, r, a, claimErr.Error())
		}
		return
	}
	_ = s.Store.UpdateAppStatusErr(r.Context(), a.ID, "updating", "")
	s.recordAudit(r, audit.ActionAppUpdate, "app", a.ID, a.Name, audit.OutcomeSuccess, "requested")
	s.startUpdateDeploy(s.backgroundAuditCtx(r), a, req, operation)
	http.Redirect(w, r, "/deployments/"+operation.ID, http.StatusSeeOther)
}

func (s *Server) renderUpdateError(w http.ResponseWriter, r *http.Request, a *store.App, message string) {
	s.Renderer.Render(w, r, "update.html", appUpdateData{
		PageData: pageCtx(r, "Update "+a.Name), App: a, SourceType: a.SourceType, Error: message,
	})
}

// updateArchiveFromRequest copies a multipart archive to a temporary file so
// the request can be handled asynchronously after the HTTP response returns.
func updateArchiveFromRequest(r *http.Request) (string, func(), error) {
	f, header, err := r.FormFile("tarball")
	if err != nil {
		return "", func() {}, errors.New("missing 'tarball' file field — choose a .tar.gz, .tgz, or .zip archive")
	}
	defer f.Close()
	if header.Size == 0 {
		return "", func() {}, errors.New("uploaded file is empty")
	}
	lower := strings.ToLower(header.Filename)
	var suffix string
	switch {
	case strings.HasSuffix(lower, ".zip"):
		suffix = ".zip"
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		suffix = ".tar.gz"
	default:
		return "", func() {}, errors.New("archive must be .tar.gz, .tgz, or .zip")
	}
	tmp, err := os.CreateTemp("", "se-update-*"+suffix)
	if err != nil {
		return "", func() {}, err
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := io.Copy(tmp, f); err != nil {
		tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func (s *Server) startUpdateDeploy(base context.Context, app *store.App, req deployer.UpdateRequest, operation *store.AppOperation) {
	d, sink, progress := s.deploySinkFor(app.ID, operation)
	s.submitDeployment(func() {
		if req.ArchivePath != "" {
			defer os.Remove(req.ArchivePath)
		}
		s.deploySem <- struct{}{}
		defer func() { <-s.deploySem }()
		var runErr error
		defer func() {
			if p := recover(); p != nil {
				runErr = fmt.Errorf("internal update panic: %v", p)
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", runErr.Error())
				sink.Append("✕ " + runErr.Error())
				progress.failure(runErr)
			}
			status := store.OperationStatusCompleted
			if runErr != nil {
				status = store.OperationStatusFailed
			}
			s.finishDeploymentOperation(operation, status, runErr)
		}()
		ctx, cancel := context.WithTimeout(base, 30*time.Minute)
		defer cancel()
		if _, err := d.UpdateClaimed(ctx, req, operation); err != nil {
			runErr = err
			sink.Append("✕ update failed: " + err.Error())
			progress.failure(err)
			return
		}
		sink.Append("✓ update complete")
		progress.success()
	})
}

func (s *Server) apiUpdateApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := jsonDecoder(r).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if a.SourceType != "git" {
		jsonError(w, http.StatusBadRequest, "archive apps require POST /apps/{name}/update/upload")
		return
	}
	operation, err := s.Store.ClaimAppOperation(r.Context(), a.ID, "update")
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			jsonError(w, http.StatusConflict, "another update or deploy is already in progress")
		} else {
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	_ = s.Store.UpdateAppStatusErr(r.Context(), a.ID, "updating", "")
	s.recordAudit(r, audit.ActionAppUpdate, "app", a.ID, a.Name, audit.OutcomeSuccess, "requested git "+strings.TrimSpace(body.Ref))
	s.startUpdateDeploy(s.backgroundAuditCtx(r), a, deployer.UpdateRequest{Name: a.Name, Ref: strings.TrimSpace(body.Ref)}, operation)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":         a.Name,
		"status":       "updating",
		"operation_id": operation.ID,
		"status_url":   "/api/v1/deployments/" + operation.ID,
	})
}

func (s *Server) apiUpdateUploadApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if a.SourceType != "drop" {
		jsonError(w, http.StatusBadRequest, "Git apps use POST /apps/{name}/update")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "could not parse multipart upload: "+err.Error())
		return
	}
	archive, _, err := updateArchiveFromRequest(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	operation, err := s.Store.ClaimAppOperation(r.Context(), a.ID, "update")
	if err != nil {
		_ = os.Remove(archive)
		if errors.Is(err, store.ErrConflict) {
			jsonError(w, http.StatusConflict, "another update or deploy is already in progress")
		} else {
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	_ = s.Store.UpdateAppStatusErr(r.Context(), a.ID, "updating", "")
	s.recordAudit(r, audit.ActionAppUpdate, "app", a.ID, a.Name, audit.OutcomeSuccess, "requested archive update")
	s.startUpdateDeploy(s.backgroundAuditCtx(r), a, deployer.UpdateRequest{Name: a.Name, ArchivePath: archive, SourceRef: strings.TrimSpace(r.FormValue("source_name"))}, operation)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":         a.Name,
		"status":       "updating",
		"operation_id": operation.ID,
		"status_url":   "/api/v1/deployments/" + operation.ID,
	})
}

// jsonDecoder keeps the body cap consistent with other API handlers without
// introducing a second request-body reader abstraction.
func jsonDecoder(r *http.Request) *json.Decoder {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20))
}
