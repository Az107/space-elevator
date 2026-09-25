package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type deploymentPageData struct {
	PageData
	App                *store.App
	Operation          *store.AppOperation
	Steps              []store.DeployStep
	Logs               []store.DeployLog
	LatestSeq          int
	Progress           int
	StepCount          int
	StatusLabel        string
	Description        string
	OperationTypeLabel string
	Active             bool
	CanRetry           bool
	CanEditBuild       bool
}

type deploymentStatusResponse struct {
	Status      string             `json:"status"`
	Label       string             `json:"label"`
	CurrentStep string             `json:"current_step,omitempty"`
	Error       string             `json:"error,omitempty"`
	Seq         int                `json:"seq"`
	Steps       []store.DeployStep `json:"steps"`
	Lines       []store.DeployLog  `json:"lines,omitempty"`
}

func boundedDeploymentError(message string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if len(message) > 2048 {
		return message[:2048] + "…"
	}
	return message
}

func boundedDeploymentLog(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 4096 {
		return message[:4096] + "…"
	}
	return message
}

func sanitizeDeploymentSteps(steps []store.DeployStep) []store.DeployStep {
	out := append([]store.DeployStep(nil), steps...)
	for i := range out {
		out[i].Error = boundedDeploymentError(out[i].Error)
	}
	return out
}

func sanitizeDeploymentLogs(logs []store.DeployLog) []store.DeployLog {
	out := append([]store.DeployLog(nil), logs...)
	for i := range out {
		out[i].Message = boundedDeploymentLog(out[i].Message)
	}
	return out
}

func (s *Server) deploymentForRequest(w http.ResponseWriter, r *http.Request) (*store.AppOperation, *store.App, bool) {
	op, err := s.Store.GetOperation(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return nil, nil, false
	}
	app, err := s.Store.GetApp(r.Context(), op.AppID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return nil, nil, false
	}
	opCopy := *op
	opCopy.Error = boundedDeploymentError(opCopy.Error)
	appCopy := *app
	appCopy.LastError = boundedDeploymentError(appCopy.LastError)
	return &opCopy, &appCopy, true
}

func deploymentCanEditBuild(app *store.App, op *store.AppOperation) bool {
	if app == nil || op == nil || app.SourceType != "git" || app.Kind != store.KindWeb {
		return false
	}
	if app.BuildMode != store.BuildModeCustom && app.BuildMode != store.BuildModeStatic {
		return false
	}
	return operationCanRetry(op)
}

func deploymentProgress(steps []store.DeployStep) int {
	if len(steps) == 0 {
		return 0
	}
	complete := 0
	for _, step := range steps {
		switch step.Status {
		case "succeeded", "skipped", "rolled_back":
			complete++
		}
	}
	return complete * 100 / len(steps)
}

func (s *Server) handleDeploymentDetail(w http.ResponseWriter, r *http.Request) {
	op, app, ok := s.deploymentForRequest(w, r)
	if !ok {
		return
	}
	steps, _ := s.Store.ListDeploySteps(r.Context(), op.ID)
	if len(steps) == 0 {
		newDeployProgress(s.Store, app, op)
		steps, _ = s.Store.ListDeploySteps(r.Context(), op.ID)
	}
	logs, latest, _ := s.Store.ListDeployLogs(r.Context(), op.ID, 0, 500)
	steps = sanitizeDeploymentSteps(steps)
	logs = sanitizeDeploymentLogs(logs)
	s.Renderer.Render(w, r, "deployment.html", deploymentPageData{
		PageData:           pageCtx(r, deploymentStatusLabel(op.Status)+" "+app.Name),
		App:                app,
		Operation:          op,
		Steps:              steps,
		Logs:               logs,
		LatestSeq:          latest,
		Progress:           deploymentProgress(steps),
		StepCount:          len(steps),
		StatusLabel:        deploymentStatusLabel(op.Status),
		Description:        deploymentStatusDescription(op),
		OperationTypeLabel: operationTypeLabel(op),
		Active:             operationIsActive(op),
		CanRetry:           operationCanRetry(op),
		CanEditBuild:       deploymentCanEditBuild(app, op),
	})
}

func (s *Server) handleDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	op, _, ok := s.deploymentForRequest(w, r)
	if !ok {
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	steps, _ := s.Store.ListDeploySteps(r.Context(), op.ID)
	logs, latest, err := s.Store.ListDeployLogs(r.Context(), op.ID, after, 500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	steps = sanitizeDeploymentSteps(steps)
	logs = sanitizeDeploymentLogs(logs)
	writeJSON(w, http.StatusOK, deploymentStatusResponse{
		Status:      op.Status,
		Label:       deploymentStatusLabel(op.Status),
		CurrentStep: op.CurrentStep,
		Error:       boundedDeploymentError(op.Error),
		Seq:         latest,
		Steps:       steps,
		Lines:       logs,
	})
}

func (s *Server) handleDeploymentRetry(w http.ResponseWriter, r *http.Request) {
	op, app, ok := s.deploymentForRequest(w, r)
	if !ok {
		return
	}
	if !operationCanRetry(op) {
		s.redirectErr(w, r, "/deployments/"+op.ID, "This deployment cannot be retried from its current state.")
		return
	}
	if op.OperationType == "update" && app.SourceType == "drop" {
		s.redirectErr(w, r, "/apps/"+app.Name+"/update", "Archive update retries need a new archive; choose Update and upload the source again.")
		return
	}

	retry, err := s.Store.ClaimAppOperation(r.Context(), app.ID, "retry")
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.redirectErr(w, r, "/deployments/"+op.ID, "another update or deploy is already in progress")
		} else {
			s.redirectErr(w, r, "/deployments/"+op.ID, err.Error())
		}
		return
	}

	if op.OperationType == "update" {
		_ = s.Store.UpdateAppStatusErr(r.Context(), app.ID, "updating", "")
		s.recordAudit(r, audit.ActionAppUpdate, "app", app.ID, app.Name, audit.OutcomeSuccess, "retry "+op.ID)
		s.startUpdateDeploy(s.backgroundAuditCtx(r), app, deployer.UpdateRequest{Name: app.Name, Ref: app.GitRef}, retry)
	} else {
		_ = s.Store.UpdateAppStatusErr(r.Context(), app.ID, "pending", "")
		s.recordAudit(r, audit.ActionAppRedeploy, "app", app.ID, app.Name, audit.OutcomeSuccess, "retry "+op.ID)
		s.startRedeploy(s.backgroundAuditCtx(r), app, retry)
	}
	http.Redirect(w, r, "/deployments/"+retry.ID, http.StatusSeeOther)
}

// handleDeploymentAPI is shared by the API routes. It intentionally keeps
// the durable operation shape identical to the HTML status endpoint.
func (s *Server) handleDeploymentAPI(w http.ResponseWriter, r *http.Request) {
	op, _, ok := s.deploymentForRequest(w, r)
	if !ok {
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	steps, _ := s.Store.ListDeploySteps(r.Context(), op.ID)
	logs, latest, err := s.Store.ListDeployLogs(r.Context(), op.ID, after, 500)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	steps = sanitizeDeploymentSteps(steps)
	logs = sanitizeDeploymentLogs(logs)
	writeJSON(w, http.StatusOK, deploymentStatusResponse{
		Status:      op.Status,
		Label:       deploymentStatusLabel(op.Status),
		CurrentStep: op.CurrentStep,
		Error:       boundedDeploymentError(op.Error),
		Seq:         latest,
		Steps:       steps,
		Lines:       logs,
	})
}
