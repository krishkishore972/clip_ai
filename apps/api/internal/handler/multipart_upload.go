// Package handler translates HTTP requests into application service calls.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/krishkishore972/clip_ai/internal/httpserver"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

const (
	maxUploadBytes = upload.MaxUploadBytes
	maxPartNumber  = 10_000
	maxSignBatch   = upload.MaxSignBatch
)

// Service errors are safe classifications, not provider error messages.
var (
	ErrUploadNotFound    = upload.ErrNotFound
	ErrUploadConflict    = upload.ErrConflict
	ErrUploadUnavailable = upload.ErrUnavailable
	ErrUploadTooLarge    = upload.ErrTooLarge
	ErrInvalidUpload     = upload.ErrInvalid
)

// Aliases keep the HTTP boundary and application service on one model.
type CreateUploadRequest = upload.CreateUploadRequest
type SignUploadPartsRequest = upload.SignUploadPartsRequest
type CompleteUploadPart = upload.CompleteUploadPart
type CompleteUploadRequest = upload.CompleteUploadRequest
type UploadedPart = upload.UploadedPart
type UploadSession = upload.UploadSession
type SignedUploadPart = upload.SignedUploadPart
type SignedUploadParts = upload.SignedUploadParts

// Create starts a fresh upload on every call; persistent deduplication is absent.
type UploadCreator interface {
	Create(context.Context, CreateUploadRequest) (UploadSession, error)
}

type UploadPartSigner interface {
	SignParts(context.Context, upload.Access, []int) (SignedUploadParts, error)
}

// UploadSessions owns state, expiration, and authoritative S3 part verification.
// Complete must validate expected sizes/parts and never launch AI processing.
type UploadSessions interface {
	Get(context.Context, upload.Access) (UploadSession, error)
	Complete(context.Context, upload.Access, CompleteUploadRequest) (UploadSession, error)
	Abort(context.Context, upload.Access) error
}

// MultipartUploadService is injected; handlers do not store sessions or call AWS.
// Production adapters must additionally enforce server-derived ownership.
type MultipartUploadService interface {
	UploadCreator
	UploadPartSigner
	UploadSessions
}

type MultipartUploadHandler struct {
	service MultipartUploadService
	logger  *slog.Logger
}

func NewMultipartUploadHandler(service MultipartUploadService, logger *slog.Logger) *MultipartUploadHandler {
	return &MultipartUploadHandler{service: service, logger: logger}
}

// Register installs JSON control endpoints, not multipart/form-data endpoints.
// The caller must restrict registration to development until auth is implemented.
func (h *MultipartUploadHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /uploads", h.Create)
	mux.HandleFunc("POST /uploads/{uploadId}/parts/sign", h.SignParts)
	mux.HandleFunc("GET /uploads/{uploadId}", h.Get)
	mux.HandleFunc("POST /uploads/{uploadId}/complete", h.Complete)
	mux.HandleFunc("DELETE /uploads/{uploadId}", h.Abort)
}

func (h *MultipartUploadHandler) Create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	in, ok := decodeUploadRequest[CreateUploadRequest](h, w, r)
	if !ok {
		return
	}
	if in.SizeBytes <= 0 || len(in.Filename) == 0 || len(in.Filename) > 255 ||
		strings.ContainsAny(in.Filename, `/\`) || strings.IndexFunc(in.Filename, unicode.IsControl) >= 0 {
		h.fail(w, r, http.StatusBadRequest, "invalid_upload", "filename and a positive sizeBytes are required")
		return
	}
	if in.SizeBytes > maxUploadBytes {
		h.fail(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the 2000000000 byte limit")
		return
	}
	want := map[string]string{".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm"}[strings.ToLower(path.Ext(in.Filename))]
	if want == "" || in.ContentType != want {
		h.fail(w, r, http.StatusBadRequest, "unsupported_media", "declare an mp4, mov, or webm video with its matching content type")
		return
	}
	if !h.available(w, r) {
		return
	}
	out, err := h.service.Create(r.Context(), *in)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	h.respond(w, r, http.StatusCreated, out)
}

func (h *MultipartUploadHandler) SignParts(w http.ResponseWriter, r *http.Request) {
	if !h.sessionID(w, r) {
		return
	}
	in, ok := decodeUploadRequest[SignUploadPartsRequest](h, w, r)
	if !ok {
		return
	}
	if len(in.PartNumbers) == 0 || len(in.PartNumbers) > maxSignBatch || !validPartNumbers(in.PartNumbers) {
		h.fail(w, r, http.StatusBadRequest, "invalid_parts", "provide 1 to 100 unique part numbers between 1 and 10000")
		return
	}
	if !h.available(w, r) {
		return
	}
	out, err := h.service.SignParts(r.Context(), access(r), in.PartNumbers)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	h.respond(w, r, http.StatusOK, out)
}

func (h *MultipartUploadHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !h.sessionID(w, r) || !h.available(w, r) {
		return
	}
	out, err := h.service.Get(r.Context(), access(r))
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	h.respond(w, r, http.StatusOK, out)
}

func (h *MultipartUploadHandler) Complete(w http.ResponseWriter, r *http.Request) {
	if !h.sessionID(w, r) {
		return
	}
	in, ok := decodeUploadRequest[CompleteUploadRequest](h, w, r)
	if !ok {
		return
	}
	if len(in.Parts) == 0 || len(in.Parts) > maxPartNumber {
		h.fail(w, r, http.StatusBadRequest, "invalid_parts", "provide 1 to 10000 parts with ETags")
		return
	}
	numbers := make([]int, 0, len(in.Parts))
	for _, part := range in.Parts {
		if strings.TrimSpace(part.ETag) == "" || len(part.ETag) > 1024 || strings.IndexFunc(part.ETag, unicode.IsControl) >= 0 {
			h.fail(w, r, http.StatusBadRequest, "invalid_parts", "each part requires an ETag of at most 1024 bytes without control characters")
			return
		}
		numbers = append(numbers, part.PartNumber)
	}
	if !validPartNumbers(numbers) {
		h.fail(w, r, http.StatusBadRequest, "invalid_parts", "part numbers must be unique and between 1 and 10000")
		return
	}
	if !h.available(w, r) {
		return
	}
	out, err := h.service.Complete(r.Context(), access(r), *in)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	h.respond(w, r, http.StatusOK, out)
}

func (h *MultipartUploadHandler) Abort(w http.ResponseWriter, r *http.Request) {
	if !h.sessionID(w, r) {
		return
	}
	if !h.available(w, r) {
		return
	}
	if err := h.service.Abort(r.Context(), access(r)); err != nil {
		h.serviceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *MultipartUploadHandler) sessionID(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if !validToken(r.PathValue("uploadId")) {
		h.fail(w, r, http.StatusBadRequest, "invalid_upload_id", "a valid upload session ID is required")
		return false
	}
	token := r.Header.Get("Upload-Token")
	if len(r.Header.Values("Upload-Token")) != 1 || len(token) == 0 || len(token) > upload.MaxTokenBytes {
		h.fail(w, r, http.StatusForbidden, "invalid_upload_token", "a valid Upload-Token header is required")
		return false
	}
	return true
}

func access(r *http.Request) upload.Access {
	return upload.Access{UploadID: r.PathValue("uploadId"), Token: r.Header.Get("Upload-Token")}
}

func validToken(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.:", c)) {
			return false
		}
	}
	return true
}

func validPartNumbers(numbers []int) bool {
	seen := make(map[int]bool, len(numbers))
	for _, n := range numbers {
		if n < 1 || n > maxPartNumber || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}

func decodeUploadRequest[T any](h *MultipartUploadHandler, w http.ResponseWriter, r *http.Request) (*T, bool) {
	in, err := httpserver.DecodeJSON[*T](w, r)
	if err != nil {
		switch {
		case errors.Is(err, httpserver.ErrUnsupportedMediaType):
			h.fail(w, r, http.StatusUnsupportedMediaType, "unsupported_content_type", "Content-Type must be application/json")
		case errors.Is(err, httpserver.ErrBodyTooLarge):
			h.fail(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "JSON body exceeds the 1 MiB limit")
		default:
			h.fail(w, r, http.StatusBadRequest, "invalid_json", "provide a single JSON object with only supported fields")
		}
		return nil, false
	}
	if in == nil {
		h.fail(w, r, http.StatusBadRequest, "invalid_json", "a JSON object is required")
		return nil, false
	}
	return in, true
}

func (h *MultipartUploadHandler) available(w http.ResponseWriter, r *http.Request) bool {
	if h.service == nil {
		h.serviceError(w, r, ErrUploadUnavailable)
		return false
	}
	return true
}

func (h *MultipartUploadHandler) serviceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, upload.ErrToken):
		h.fail(w, r, http.StatusForbidden, "invalid_upload_token", "upload token is invalid or expired")
	case errors.Is(err, ErrUploadNotFound):
		h.fail(w, r, http.StatusNotFound, "upload_not_found", "upload session not found")
	case errors.Is(err, ErrUploadConflict):
		h.fail(w, r, http.StatusConflict, "upload_conflict", "upload session is not in the required state")
	case errors.Is(err, ErrUploadUnavailable):
		h.fail(w, r, http.StatusServiceUnavailable, "upload_service_unavailable", "upload service is not connected or is temporarily unavailable")
	case errors.Is(err, ErrUploadTooLarge):
		h.fail(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "upload exceeds the size limit")
	case errors.Is(err, ErrInvalidUpload):
		h.fail(w, r, http.StatusBadRequest, "invalid_upload", "upload metadata or parts are invalid")
	default:
		// Do not log provider errors, which may contain signed URLs or user data.
		h.logger.ErrorContext(r.Context(), "upload service failed", "trace_id", httpserver.TraceIDFromContext(r.Context()))
		h.fail(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (h *MultipartUploadHandler) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if err := httpserver.WriteError(w, r, status, code, message, nil); err != nil {
		h.logger.ErrorContext(r.Context(), "write upload error response failed", "trace_id", httpserver.TraceIDFromContext(r.Context()))
	}
}

func (h *MultipartUploadHandler) respond(w http.ResponseWriter, r *http.Request, status int, body any) {
	if err := httpserver.WriteJSON(w, status, body); err != nil {
		h.logger.ErrorContext(r.Context(), "write upload response failed", "trace_id", httpserver.TraceIDFromContext(r.Context()))
	}
}
