package httpserver

import (
	"net/http"
)

// APIError is the error body shape every failing endpoint returns. Codes are
// stable and machine-readable; messages are human-readable and safe to log.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
	TraceID string `json:"traceId,omitempty"`
}

// WriteError writes an APIError response. details may be nil. As with WriteJSON,
// returned encoding/write errors are for logging, not writing another response.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, details any) error {
	return WriteJSON(w, status, APIError{
		Code:    code,
		Message: message,
		Details: details,
		TraceID: TraceIDFromContext(r.Context()),
	})
}
