package job

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

const (
	ValidationTaskType    = "media:validate"
	ValidationQueue       = "validation"
	ValidationMaxAttempts = 4
	ValidationMaxRetry    = 3
)

var (
	ErrValidationDatabase  = errors.New("validation database operation failed")
	ErrValidationBusy      = errors.New("validation claim is busy")
	ErrValidationStale     = errors.New("validation claim is stale")
	ErrValidationArguments = errors.New("invalid validation store arguments")
	validationCodePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

type ValidationAsset struct {
	ID, Bucket, Key, Token string
	SizeBytes              int64
	Attempts               int
}

type ValidationOutcome struct {
	Status, Code string
	Metadata     *media.Metadata
}

type ValidationStore interface {
	Claim(context.Context, string, string, time.Duration) (ValidationAsset, error)
	Finish(context.Context, ValidationAsset, ValidationOutcome) error
	Recover(context.Context, int) (int, error)
}

type SQLValidationStore struct {
	DB               *sql.DB
	PrepareValidated bool
}

// Claim locks only while reading and updating durable execution state. Object
// downloads and probing must happen after this transaction has committed.
func (s SQLValidationStore) Claim(ctx context.Context, id, token string, lease time.Duration) (ValidationAsset, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(token) == "" || lease < time.Microsecond {
		return ValidationAsset{}, ErrValidationArguments
	}
	if s.DB == nil {
		return ValidationAsset{}, ErrValidationDatabase
	}
	txCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(txCtx, nil)
	if err != nil {
		return ValidationAsset{}, validationDBError(ctx, txCtx)
	}
	defer tx.Rollback()
	const selectQuery = `
		SELECT
			id,
			storage_bucket,
			storage_key,
			size_bytes,
			validation_attempts,
			status,
			COALESCE(validation_lease_until > clock_timestamp(), false)
		FROM media_assets
		WHERE id = $1
			AND user_id IS NOT NULL
		FOR UPDATE
	`
	var asset ValidationAsset
	var status string
	var live bool
	err = tx.QueryRowContext(txCtx, selectQuery, id).Scan(
		&asset.ID,
		&asset.Bucket,
		&asset.Key,
		&asset.SizeBytes,
		&asset.Attempts,
		&status,
		&live,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidationAsset{}, nil
	}
	if err != nil {
		return ValidationAsset{}, validationDBError(ctx, txCtx)
	}
	if status != "uploaded_unvalidated" && status != "validating" {
		return ValidationAsset{}, nil
	}
	if status == "validating" && live {
		return ValidationAsset{}, ErrValidationBusy
	}
	if asset.Attempts >= ValidationMaxAttempts {
		const exhaustedQuery = `
			UPDATE media_assets
			SET
				status = 'validation_failed',
				failure_code = 'VALIDATION_ATTEMPTS_EXHAUSTED',
				validation_claim_token = NULL,
				validation_lease_until = NULL,
				updated_at = clock_timestamp()
			WHERE id = $1
		`
		if _, err := tx.ExecContext(txCtx, exhaustedQuery, id); err != nil {
			return ValidationAsset{}, validationDBError(ctx, txCtx)
		}
		if err := tx.Commit(); err != nil {
			return ValidationAsset{}, validationDBError(ctx, txCtx)
		}
		return ValidationAsset{}, nil
	}
	const claimQuery = `
		UPDATE media_assets
		SET
			status = 'validating',
			validation_attempts = validation_attempts + 1,
			validation_claim_token = $2,
			validation_lease_until = clock_timestamp() + $3 * INTERVAL '1 microsecond',
			failure_code = NULL,
			updated_at = clock_timestamp()
		WHERE id = $1
	`
	if _, err := tx.ExecContext(txCtx, claimQuery, id, token, lease.Microseconds()); err != nil {
		return ValidationAsset{}, validationDBError(ctx, txCtx)
	}
	if err := tx.Commit(); err != nil {
		return ValidationAsset{}, validationDBError(ctx, txCtx)
	}
	asset.Token = token
	asset.Attempts++
	return asset, nil
}

func (s SQLValidationStore) Finish(ctx context.Context, asset ValidationAsset, outcome ValidationOutcome) error {
	if strings.TrimSpace(asset.ID) == "" || strings.TrimSpace(asset.Token) == "" {
		return ErrValidationArguments
	}
	var metadata []byte
	var duration, width, height, num, den any
	var video, audio any
	var code any
	if outcome.Status == "ready" {
		m := outcome.Metadata
		if outcome.Code != "" || !validStoredMetadata(m) {
			return ErrValidationArguments
		}
		var err error
		metadata, err = json.Marshal(m)
		if err != nil {
			return ErrValidationArguments
		}
		v := m.Video[0]
		duration, width, height, num, den = m.DurationMs, v.Width, v.Height, v.FPS.Num, v.FPS.Den
		video, audio = v.Codec, m.Audio[0].Codec
	} else {
		switch outcome.Status {
		case "uploaded_unvalidated", "rejected", "validation_failed":
		default:
			return ErrValidationArguments
		}
		if outcome.Metadata != nil || !validationCodePattern.MatchString(outcome.Code) {
			return ErrValidationArguments
		}
		code = outcome.Code
	}
	if s.DB == nil {
		return ErrValidationDatabase
	}
	finishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(finishCtx, nil)
	if err != nil {
		return validationDBError(ctx, finishCtx)
	}
	defer tx.Rollback()
	// Acquire the row lock before checking database time: a lock wait must not
	// allow a lease that expired during that wait to authorize the write.
	const lockQuery = `
		SELECT id
		FROM media_assets
		WHERE id = $1
		FOR UPDATE
	`
	var lockedID string
	if err := tx.QueryRowContext(finishCtx, lockQuery, asset.ID).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrValidationStale
		}
		return validationDBError(ctx, finishCtx)
	}
	const query = `
		UPDATE media_assets
		SET
			status = $3,
			failure_code = $4,
			probed_metadata = $5,
			duration_ms = $6,
			width = $7,
			height = $8,
			fps_num = $9,
			fps_den = $10,
			video_codec = $11,
			audio_codec = $12,
			validated_at = CASE WHEN $3 = 'ready' THEN clock_timestamp() ELSE NULL END,
			validation_claim_token = NULL,
			validation_lease_until = NULL,
			updated_at = clock_timestamp()
		WHERE id = $1
			AND validation_claim_token = $2
			AND status = 'validating'
			AND validation_lease_until > clock_timestamp()
			AND user_id IS NOT NULL
	`
	result, err := tx.ExecContext(
		finishCtx,
		query,
		asset.ID,
		asset.Token,
		outcome.Status,
		code,
		metadata,
		duration,
		width,
		height,
		num,
		den,
		video,
		audio,
	)
	if err != nil {
		return validationDBError(ctx, finishCtx)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return validationDBError(ctx, finishCtx)
	}
	if rows == 0 {
		return ErrValidationStale
	}
	if outcome.Status == "ready" && s.PrepareValidated {
		if err := preparation.InsertPending(finishCtx, tx, asset.ID); err != nil {
			return validationDBError(ctx, finishCtx)
		}
	}
	if outcome.Status == "uploaded_unvalidated" {
		// Asynq may have requeued and acknowledged a busy duplicate before a
		// canceled owner finishes. Persist a delayed backup event atomically;
		// never rely solely on that owner's eventual return value to retry.
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return ErrValidationDatabase
		}
		const retryEventQuery = `
			INSERT INTO outbox_events (
				id,
				asset_id,
				task_type,
				queue_name,
				payload,
				dedupe_key,
				next_attempt_at
			)
			SELECT
				$2,
				id,
				'media:validate',
				'validation',
				jsonb_build_object('assetId', id),
				'validate:' || id || ':retry:' || validation_attempts::text,
				clock_timestamp() + CASE validation_attempts
					WHEN 1 THEN INTERVAL '1 minute'
					WHEN 2 THEN INTERVAL '2 minutes'
					ELSE INTERVAL '4 minutes'
				END
			FROM media_assets
			WHERE id = $1
			ON CONFLICT (dedupe_key) DO NOTHING
		`
		if _, err := tx.ExecContext(finishCtx, retryEventQuery, asset.ID, hex.EncodeToString(random[:])); err != nil {
			return validationDBError(ctx, finishCtx)
		}
	}
	if err := tx.Commit(); err != nil {
		return validationDBError(ctx, finishCtx)
	}
	return nil
}

// Recover makes interrupted attempts discoverable even when Redis has archived
// their old tasks. The state change and identifier-only event commit together.
func (s SQLValidationStore) Recover(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, ErrValidationArguments
	}
	limit = min(limit, 100)
	if s.DB == nil {
		return 0, ErrValidationDatabase
	}
	txCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(txCtx, nil)
	if err != nil {
		return 0, validationDBError(ctx, txCtx)
	}
	defer tx.Rollback()
	const selectQuery = `
		SELECT
			id,
			validation_attempts
		FROM media_assets
		WHERE status = 'validating'
			AND user_id IS NOT NULL
			AND (validation_lease_until IS NULL OR validation_lease_until <= clock_timestamp())
		ORDER BY validation_lease_until NULLS FIRST, id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.QueryContext(txCtx, selectQuery, limit)
	if err != nil {
		return 0, validationDBError(ctx, txCtx)
	}
	type interrupted struct {
		id       string
		attempts int
	}
	var assets []interrupted
	for rows.Next() {
		var a interrupted
		if err := rows.Scan(&a.id, &a.attempts); err != nil {
			rows.Close()
			return 0, validationDBError(ctx, txCtx)
		}
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, validationDBError(ctx, txCtx)
	}
	const updateQuery = `
		UPDATE media_assets
		SET
			status = $2,
			failure_code = $3,
			validation_claim_token = NULL,
			validation_lease_until = NULL,
			updated_at = clock_timestamp()
		WHERE id = $1
	`
	const eventQuery = `
		INSERT INTO outbox_events (
			id,
			asset_id,
			task_type,
			queue_name,
			payload,
			dedupe_key
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (dedupe_key) DO NOTHING
	`
	for _, a := range assets {
		status, code := "uploaded_unvalidated", "VALIDATION_INTERRUPTED"
		if a.attempts >= ValidationMaxAttempts {
			status, code = "validation_failed", "VALIDATION_ATTEMPTS_EXHAUSTED"
		}
		if _, err := tx.ExecContext(txCtx, updateQuery, a.id, status, code); err != nil {
			return 0, validationDBError(ctx, txCtx)
		}
		if status == "validation_failed" {
			continue
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return 0, ErrValidationDatabase
		}
		payload, err := json.Marshal(struct {
			AssetID string `json:"assetId"`
		}{a.id})
		if err != nil {
			return 0, ErrValidationDatabase
		}
		if _, err := tx.ExecContext(
			txCtx,
			eventQuery,
			hex.EncodeToString(random[:]),
			a.id,
			ValidationTaskType,
			ValidationQueue,
			payload,
			fmt.Sprintf("validate:%s:recovery:%d", a.id, a.attempts),
		); err != nil {
			return 0, validationDBError(ctx, txCtx)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, validationDBError(ctx, txCtx)
	}
	return len(assets), nil
}

func validationDBError(parent, operation context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := operation.Err(); err != nil {
		return err
	}
	return ErrValidationDatabase
}

func validStoredMetadata(m *media.Metadata) bool {
	if m == nil || m.DurationMs <= 0 || len(m.Video) == 0 || len(m.Audio) == 0 {
		return false
	}
	for _, v := range m.Video {
		if v.Width <= 0 || v.Height <= 0 || v.FPS.Num <= 0 || v.FPS.Den <= 0 || strings.TrimSpace(v.Codec) == "" {
			return false
		}
	}
	for _, a := range m.Audio {
		if strings.TrimSpace(a.Codec) == "" || a.SampleRate <= 0 || a.Channels <= 0 {
			return false
		}
	}
	return true
}
