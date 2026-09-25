package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/albertoruiz/space-elevator/internal/store"
)

var errAppOperationActive = errors.New("another deployment or update is already in progress")

func (s *Server) requireIdleApp(ctx context.Context, appID string) error {
	if s.Store == nil {
		return nil
	}
	active, err := s.Store.ActiveOperation(ctx, appID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if active != nil {
		return fmt.Errorf("%w: operation %s is %s", errAppOperationActive, active.ID, active.Status)
	}
	return nil
}

func (s *Server) guardHTMLIdle(w http.ResponseWriter, r *http.Request, appID string) bool {
	err := s.requireIdleApp(r.Context(), appID)
	if err == nil {
		return true
	}
	s.redirectErr(w, r, "/apps", err.Error())
	return false
}

func (s *Server) guardAPIIdle(w http.ResponseWriter, r *http.Request, appID string) bool {
	err := s.requireIdleApp(r.Context(), appID)
	if err == nil {
		return true
	}
	status := http.StatusConflict
	if !errors.Is(err, errAppOperationActive) {
		status = http.StatusInternalServerError
	}
	jsonError(w, status, err.Error())
	return false
}
