package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type buildSettingsData struct {
	PageData
	App   *store.App
	Error string
}

func (s *Server) handleBuildSettingsForm(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if a.SourceType != "git" || a.Kind != store.KindWeb {
		s.redirectErr(w, r, "/apps/"+a.Name, "Build settings are only editable for Git web apps.")
		return
	}
	operations, _ := s.Store.ListOperations(r.Context(), a.ID)
	if len(operations) > 0 && !operationCanRetry(operations[0]) {
		s.redirectErr(w, r, "/deployments/"+operations[0].ID, "Build settings can only be changed after a failed or rolled-back deployment.")
		return
	}
	a.LastError = boundedDeploymentError(a.LastError)
	s.Renderer.Render(w, r, "build_settings.html", buildSettingsData{PageData: pageCtx(r, "Build settings "+a.Name), App: a})
}

func (s *Server) handleBuildSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if a.SourceType != "git" || a.Kind != store.KindWeb {
		s.redirectErr(w, r, "/apps/"+a.Name, "Build settings are only editable for Git web apps.")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderBuildSettingsError(w, r, a, "Could not parse build settings: "+err.Error())
		return
	}
	operations, _ := s.Store.ListOperations(r.Context(), a.ID)
	if len(operations) > 0 && !operationCanRetry(operations[0]) {
		s.redirectErr(w, r, "/deployments/"+operations[0].ID, "Build settings can only be changed after a failed or rolled-back deployment.")
		return
	}

	mode := strings.TrimSpace(r.FormValue("build_mode"))
	if mode == "" {
		mode = store.BuildModeCustom
	}
	image := strings.TrimSpace(r.FormValue("image"))
	buildCommand := strings.TrimSpace(r.FormValue("build_cmd"))
	runCommand := strings.TrimSpace(r.FormValue("run_cmd"))
	servePath := strings.TrimSpace(r.FormValue("serve_path"))
	portValue := strings.TrimSpace(r.FormValue("port"))
	port := 0
	var portErr error
	if portValue == "" {
		if mode == store.BuildModeStatic {
			port = builder.DefaultStaticListenPort
		} else {
			port = builder.DefaultListenPort
		}
	} else {
		port, portErr = builder.ParsePort(portValue)
	}
	if portErr != nil {
		s.renderBuildSettingsError(w, r, a, portErr.Error())
		return
	}

	if mode == store.BuildModeStatic {
		if image == "" {
			image = "node:20-bookworm"
		}
		sb := builder.StaticBuild{BuilderImage: image, BuildCommand: buildCommand, ServePath: servePath, ListenPort: port}
		if err := sb.Validate(); err != nil {
			s.renderBuildSettingsError(w, r, a, "static build: "+err.Error())
			return
		}
		a.BuildMode = store.BuildModeStatic
		a.BuilderImage = image
		a.BuildCommand = buildCommand
		a.RunCommand = ""
		a.ServePath = servePath
		a.ListenPort = port
	} else if mode == store.BuildModeCustom || mode == "server" {
		cb := builder.CustomBuild{BuilderImage: image, BuildCommand: buildCommand, RunCommand: runCommand, ListenPort: port}
		if err := cb.Validate(); err != nil {
			s.renderBuildSettingsError(w, r, a, "custom build: "+err.Error())
			return
		}
		a.BuildMode = store.BuildModeCustom
		a.BuilderImage = image
		a.BuildCommand = buildCommand
		a.RunCommand = runCommand
		a.ServePath = ""
		a.ListenPort = port
	} else {
		s.renderBuildSettingsError(w, r, a, fmt.Sprintf("unknown build mode %q", mode))
		return
	}

	a.Status = "pending"
	if err := s.Store.UpdateApp(r.Context(), a); err != nil {
		s.renderBuildSettingsError(w, r, a, err.Error())
		return
	}
	operation, err := s.Store.ClaimAppOperation(r.Context(), a.ID, "retry")
	if err != nil {
		s.redirectErr(w, r, "/deployments/"+firstOperationID(operations), err.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppRedeploy, "app", a.ID, a.Name, audit.OutcomeSuccess, "build settings changed; retry "+firstOperationID(operations))
	if len(operations) > 0 && operations[0].OperationType == "update" && a.SourceType == "git" {
		s.startUpdateDeploy(s.backgroundAuditCtx(r), a, deployer.UpdateRequest{Name: a.Name, Ref: a.GitRef}, operation)
	} else {
		s.startRedeploy(s.backgroundAuditCtx(r), a, operation)
	}
	http.Redirect(w, r, "/deployments/"+operation.ID, http.StatusSeeOther)
}

func (s *Server) renderBuildSettingsError(w http.ResponseWriter, r *http.Request, a *store.App, message string) {
	a.LastError = boundedDeploymentError(a.LastError)
	s.Renderer.Render(w, r, "build_settings.html", buildSettingsData{PageData: pageCtx(r, "Build settings "+a.Name), App: a, Error: message})
}

func firstOperationID(operations []*store.AppOperation) string {
	if len(operations) == 0 {
		return ""
	}
	return operations[0].ID
}
