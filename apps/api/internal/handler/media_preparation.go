package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/httpserver"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

// PreparationStore receives only the authenticated internal owner, never a
// client-supplied owner or storage location. Reads cannot enqueue work.
type PreparationStore interface {
	GetStatus(context.Context, string, string) (preparation.Status, error)
	Ensure(context.Context, string, string) (preparation.Status, error)
}

type MediaPreparationHandler struct {
	store   PreparationStore
	enabled bool
	logger  *slog.Logger
}

func NewMediaPreparationHandler(store PreparationStore, enabled bool, logger *slog.Logger) *MediaPreparationHandler {
	return &MediaPreparationHandler{store: store, enabled: enabled, logger: logger}
}

func (h *MediaPreparationHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /media/{assetId}/preparation", h.Get)
	mux.HandleFunc("POST /media/{assetId}/preparation", h.Ensure)
}

func (h *MediaPreparationHandler) owner(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		_ = httpserver.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue.", nil)
		return "", "", false
	}
	id := r.PathValue("assetId")
	if !media.ValidAssetID(id) {
		_ = httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_asset_id", "A valid asset ID is required.", nil)
		return "", "", false
	}
	return principal.UserID, id, true
}

func (h *MediaPreparationHandler) Get(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := h.owner(w, r)
	if !ok {
		return
	}
	if h.store == nil {
		h.respondError(w, r, preparation.ErrDatabase)
		return
	}
	if !h.enabled {
		h.respondError(w, r, preparation.ErrDisabled)
		return
	}
	result, err := h.store.GetStatus(r.Context(), owner, id)
	h.respondStatus(w, r, id, result, err, http.StatusOK)
}

func (h *MediaPreparationHandler) Ensure(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := h.owner(w, r)
	if !ok {
		return
	}
	if !h.enabled {
		h.respondError(w, r, preparation.ErrDisabled)
		return
	}
	// This idempotent operation accepts no body, owner ID, retry override or URL.
	if r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(data) != 0 {
			_ = httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_preparation_request", "This request does not accept a body.", nil)
			return
		}
	}
	if h.store == nil {
		h.respondError(w, r, preparation.ErrDatabase)
		return
	}
	result, err := h.store.Ensure(r.Context(), owner, id)
	h.respondStatus(w, r, id, result, err, http.StatusAccepted)
}

func (h *MediaPreparationHandler) respondStatus(w http.ResponseWriter, r *http.Request, id string, result preparation.Status, err error, status int) {
	if err == nil {
		result, err = preparation.SanitizeStatus(result)
		if err == nil && result.AssetID != id {
			err = preparation.ErrDatabase
		}
	}
	if err != nil {
		h.respondError(w, r, err)
		return
	}
	_ = httpserver.WriteJSON(w, status, result)
}

func (h *MediaPreparationHandler) respondError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, preparation.ErrNotFound):
		_ = httpserver.WriteError(w, r, http.StatusNotFound, "media_not_found", "Media asset not found.", nil)
	case errors.Is(err, preparation.ErrNotReady):
		_ = httpserver.WriteError(w, r, http.StatusConflict, "media_not_ready", "This video is not ready for the requested operation.", nil)
	case errors.Is(err, preparation.ErrDisabled):
		_ = httpserver.WriteError(w, r, http.StatusServiceUnavailable, "preparation_disabled", "Media preparation is not enabled.", nil)
	default:
		if h.logger != nil {
			h.logger.ErrorContext(r.Context(), "media preparation unavailable", "trace_id", httpserver.TraceIDFromContext(r.Context()))
		}
		_ = httpserver.WriteError(w, r, http.StatusServiceUnavailable, "preparation_unavailable", "Media preparation is temporarily unavailable.", nil)
	}
}
