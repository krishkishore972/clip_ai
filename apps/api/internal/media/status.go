package media

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
)

const (
	StatusUploadedUnvalidated = "uploaded_unvalidated"
	StatusValidating          = "validating"
	StatusReady               = "ready"
	StatusRejected            = "rejected"
	StatusValidationFailed    = "validation_failed"
)

var (
	ErrStatusUnauthenticated = errors.New("media status: authentication required")
	ErrStatusInvalid         = errors.New("media status: invalid asset ID")
	ErrStatusNotFound        = errors.New("media status: not found")
	ErrStatusUnavailable     = errors.New("media status: unavailable")
	assetIDPattern           = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
)

// AssetStatus is a status-only projection. Nullable fields are always serialized.
type AssetStatus struct {
	AssetID     string     `json:"assetId"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	FailureCode *string    `json:"failureCode"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ValidatedAt *time.Time `json:"validatedAt"`
}

func ValidAssetID(id string) bool { return assetIDPattern.MatchString(id) }

// SQLStatusReader never changes state or accesses object storage.
type SQLStatusReader struct{ DB *sql.DB }

func (s SQLStatusReader) GetStatus(parent context.Context, id string) (AssetStatus, error) {
	var out AssetStatus
	principal, ok := auth.FromContext(parent)
	if !ok {
		return out, ErrStatusUnauthenticated
	}
	if !ValidAssetID(id) {
		return out, ErrStatusInvalid
	}
	if s.DB == nil {
		return out, ErrStatusUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	const query = `
		SELECT
			id,
			status,
			validation_attempts,
			failure_code,
			updated_at,
			validated_at
		FROM media_assets
		WHERE id = $1
			AND user_id = $2
	`
	var failure sql.NullString
	var validated sql.NullTime
	err := s.DB.QueryRowContext(ctx, query, id, principal.UserID).Scan(
		&out.AssetID, &out.Status, &out.Attempts, &failure, &out.UpdatedAt, &validated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetStatus{}, ErrStatusNotFound
	}
	if err != nil {
		// Never propagate raw driver errors, which may contain credentials or SQL.
		return AssetStatus{}, ErrStatusUnavailable
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	if validated.Valid {
		t := validated.Time.UTC()
		out.ValidatedAt = &t
	}
	if failure.Valid {
		out.FailureCode = &failure.String
	}
	return PublicAssetStatus(out)
}

// PublicAssetStatus rejects corrupt projections and strips unknown persisted
// failure text at the HTTP boundary as well as the SQL boundary.
func PublicAssetStatus(in AssetStatus) (AssetStatus, error) {
	switch in.Status {
	case StatusUploadedUnvalidated, StatusValidating, StatusReady, StatusRejected, StatusValidationFailed:
	default:
		return AssetStatus{}, ErrStatusUnavailable
	}
	if !ValidAssetID(in.AssetID) || in.Attempts < 0 || in.Attempts > 4 || in.UpdatedAt.IsZero() ||
		(in.Status == StatusReady && in.ValidatedAt == nil) ||
		(in.ValidatedAt != nil && in.ValidatedAt.IsZero()) {
		return AssetStatus{}, ErrStatusUnavailable
	}
	in.UpdatedAt = in.UpdatedAt.UTC()
	if in.ValidatedAt != nil {
		t := in.ValidatedAt.UTC()
		in.ValidatedAt = &t
	}
	if in.Status == StatusReady {
		in.FailureCode = nil
	} else if in.FailureCode != nil {
		code := safeValidationCode(*in.FailureCode)
		in.FailureCode = &code
	}
	return in, nil
}

func safeValidationCode(code string) string {
	switch code {
	case "INVALID_MEDIA", "INVALID_MEDIA_METADATA", "VALIDATION_RETRIES_EXHAUSTED", "UNSUPPORTED_MEDIA", "DURATION_LIMIT", "UNSUPPORTED_VIDEO_CODEC",
		"RESOLUTION_LIMIT", "UNSUPPORTED_AUDIO_CODEC", "VIDEO_REQUIRED", "AUDIO_REQUIRED",
		"FILE_TOO_LARGE", "SIZE_MISMATCH", "STORAGE_CONFIGURATION", "TEMP_STORAGE_UNAVAILABLE",
		"DOWNLOAD_FAILED", "DOWNLOAD_TIMEOUT", "SOURCE_NOT_FOUND", "PROBE_FAILED", "PROBE_TIMEOUT",
		"PROBE_UNAVAILABLE", "VALIDATION_INTERRUPTED", "VALIDATION_TIMEOUT", "VALIDATION_ATTEMPTS_EXHAUSTED",
		"VALIDATION_FAILED", "VALIDATION_UNAVAILABLE", "VALIDATION_DATABASE":
		return code
	default:
		return "VALIDATION_FAILED"
	}
}
