// Package handler translates HTTP requests into application service calls.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/krishkishore972/clip_ai/internal/httpserver"
)

const (
	maxUploadBytes = 2_000_000_000
	maxPartNumber  = 10_000
	maxSignBatch   = 100
)

// Service errors are safe classifications, not provider error messages.
var (
	ErrUploadNotFound    = errors.New("upload not found")
	ErrUploadConflict    = errors.New("upload state conflict")
	ErrUploadUnavailable = errors.New("upload service unavailable")
	ErrUploadTooLarge    = errors.New("upload exceeds size limit")
	ErrInvalidUpload     = errors.New("invalid upload")
)

// CreateUploadRequest contains advisory metadata, not verified media facts.
type CreateUploadRequest struct {
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"sizeBytes"`
	ContentType string `json:"contentType"`
}

type SignUploadPartsRequest struct {
	PartNumbers []int `json:"partNumbers"`
}

type CompleteUploadPart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"eTag"`
}

type CompleteUploadRequest struct {
	Parts []CompleteUploadPart `json:"parts"`
}

type UploadedPart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"eTag"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// UploadID is our opaque session ID, not a client-selected S3 key or AWS upload ID.
// Completed sessions remain uploaded_unvalidated; completion cannot approve media.
type UploadSession struct {
	UploadID      string         `json:"uploadId"`
	Status        string         `json:"status"`
	PartSizeBytes int64          `json:"partSizeBytes"`
	PartCount     int            `json:"partCount"`
	ExpiresAt     time.Time      `json:"expiresAt"`
	UploadedParts []UploadedPart `json:"uploadedParts"`
}

type SignedUploadPart struct {
	PartNumber int               `json:"partNumber"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	ExpiresAt  time.Time         `json:"expiresAt"`
}

type SignedUploadParts struct {
	Parts []SignedUploadPart `json:"parts"`
}

// UploadCreator must implement replay safety for the supplied idempotency key.
type UploadCreator interface {
	Create(context.Context, string, CreateUploadRequest) (UploadSession, error)
}

type UploadPartSigner interface {
	SignParts(context.Context, string, []int) (SignedUploadParts, error)
}

// UploadSessions owns state, expiration, and authoritative S3 part verification.
// Complete must validate expected sizes/parts and never launch AI processing.
type UploadSessions interface {
	Get(context.Context, string) (UploadSession, error)
	Complete(context.Context, string, string, CompleteUploadRequest) (UploadSession, error)
	Abort(context.Context, string, string) error
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
	key, ok := h.idempotencyKey(w, r)
	if !ok {
		return
	}
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
	out, err := h.service.Create(r.Context(), key, *in)
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
	out, err := h.service.SignParts(r.Context(), r.PathValue("uploadId"), in.PartNumbers)
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
	out, err := h.service.Get(r.Context(), r.PathValue("uploadId"))
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
	key, ok := h.idempotencyKey(w, r)
	if !ok {
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
	out, err := h.service.Complete(r.Context(), r.PathValue("uploadId"), key, *in)
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
	key, ok := h.idempotencyKey(w, r)
	if !ok || !h.available(w, r) {
		return
	}
	if err := h.service.Abort(r.Context(), r.PathValue("uploadId"), key); err != nil {
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
	return true
}

func (h *MultipartUploadHandler) idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if !validToken(key) {
		h.fail(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must contain 1 to 128 letters, digits, or _-.: characters")
		return "", false
	}
	return key, true
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
