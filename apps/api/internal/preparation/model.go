// Package preparation exposes owned preparation status and validates derivatives.
// Worker state transitions are owned by the combined job.SQLProcessStore.
package preparation

import (
	"errors"
	"time"
)

const (
	Version     = 1
	MaxAttempts = 2
)

var (
	ErrNotFound  = errors.New("preparation not found")
	ErrNotReady  = errors.New("preparation source or output not ready")
	ErrDisabled  = errors.New("preparation disabled")
	ErrDatabase  = errors.New("preparation database unavailable")
	ErrArguments = errors.New("invalid preparation arguments")
)

type Status struct {
	AssetID     string     `json:"assetId"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	FailureCode *string    `json:"failureCode"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	PreparedAt  *time.Time `json:"preparedAt"`
}

type Artifacts struct {
	Bucket         string
	AudioKey       string
	AudioSHA256    string
	SourceSHA256   string
	AudioSizeBytes int64
	// AudioStartMs is the source timestamp of WAV sample zero. It may be
	// negative and must not be clamped.
	AudioStartMs int64
}

// SafeFailureCode never exposes arbitrary persisted text to API callers.
func SafeFailureCode(code string) string {
	switch code {
	case "PREPARATION_INTERRUPTED",
		"PREPARATION_ATTEMPTS_EXHAUSTED",
		"PREPARATION_RETRIES_EXHAUSTED",
		"PREPARATION_DOWNLOAD_FAILED",
		"PREPARATION_ENCODE_FAILED",
		"PREPARATION_UPLOAD_FAILED",
		"PREPARATION_TIMEOUT",
		"PREPARATION_INVALID_OUTPUT",
		"PREPARATION_SOURCE_CHANGED",
		"PREPARATION_STORAGE_FAILED",
		"PREPARATION_DISK_SPACE":
		return code
	default:
		return "PREPARATION_FAILED"
	}
}
