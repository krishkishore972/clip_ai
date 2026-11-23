package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/httpserver"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

// MediaStatusReader is deliberately read-only; a status request cannot retry work.
type MediaStatusReader interface {
	GetStatus(context.Context, string) (media.AssetStatus, error)
}

type MediaStatusHandler struct {
	reader MediaStatusReader
	logger *slog.Logger
}

// mediaStatusResponse adds the active attempt budget to the persisted
// validation projection. The budget is processing policy, not stored state, so
// clients must not assume the historical attempts ceiling is the live budget.
type mediaStatusResponse struct {
	media.AssetStatus
	MaxAttempts int `json:"maxAttempts"`
}

func NewMediaStatusHandler(reader MediaStatusReader, logger *slog.Logger) *MediaStatusHandler {
	return &MediaStatusHandler{reader: reader, logger: logger}
}

func (h *MediaStatusHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /media/{assetId}/status", h.Get)
}

func (h *MediaStatusHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := auth.FromContext(r.Context()); !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		_ = httpserver.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue.", nil)
		return
	}
	id := r.PathValue("assetId")
	if !media.ValidAssetID(id) {
		_ = httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_asset_id", "A valid asset ID is required.", nil)
		return
	}
	var result media.AssetStatus
	err := media.ErrStatusUnavailable
	if h.reader != nil {
		result, err = h.reader.GetStatus(r.Context(), id)
	}
	if err == nil {
		result, err = media.PublicAssetStatus(result)
		if err == nil && result.AssetID != id {
			err = media.ErrStatusUnavailable
		}
	}
	switch {
	case err == nil:
		_ = httpserver.WriteJSON(w, http.StatusOK, mediaStatusResponse{
			AssetStatus: result,
			MaxAttempts: preparation.MaxAttempts,
		})
	case errors.Is(err, media.ErrStatusNotFound):
		_ = httpserver.WriteError(w, r, http.StatusNotFound, "media_not_found", "Media asset not found.", nil)
	case errors.Is(err, media.ErrStatusUnauthenticated):
		w.Header().Set("WWW-Authenticate", "Bearer")
		_ = httpserver.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue.", nil)
	default:
		if h.logger != nil {
			h.logger.ErrorContext(r.Context(), "media status unavailable", "trace_id", httpserver.TraceIDFromContext(r.Context()))
		}
		_ = httpserver.WriteError(w, r, http.StatusServiceUnavailable, "media_status_unavailable", "Media status is temporarily unavailable.", nil)
	}
}
