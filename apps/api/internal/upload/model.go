// Package upload implements the stateless, development-only multipart flow.
// Upload tokens are bearer capabilities, not authenticated user sessions.
package upload

import (
	"errors"
	"time"
)

const (
	MaxUploadBytes int64 = 2_000_000_000
	PartSizeBytes  int64 = 16 << 20
	MaxSignBatch         = 100
	MaxTokenBytes        = 4096
)

var (
	ErrNotFound    = errors.New("upload not found")
	ErrConflict    = errors.New("upload state conflict")
	ErrUnavailable = errors.New("upload service unavailable")
	ErrTooLarge    = errors.New("upload exceeds size limit")
	ErrInvalid     = errors.New("invalid upload")
	ErrToken       = errors.New("invalid or expired upload token")
	ErrUnauthenticated = errors.New("authentication required")
)

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

type UploadSession struct {
	UploadID      string         `json:"uploadId"`
	UploadToken   string         `json:"uploadToken"`
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

// Access is supplied explicitly; it must never be logged or trusted without
// verifying the token's signature, expiry, scope, and matching path ID.
type Access struct {
	UploadID string
	Token    string
}
