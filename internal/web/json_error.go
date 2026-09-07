package web

import (
	"encoding/json"
	"net/http"
)

// jsonError writes an {"error": "..."} JSON body with the given status.
// Used by API endpoints (e.g. /apps/drop) so the drag-drop UI can show a
// useful message instead of the generic "Bad Request" status text.
func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
