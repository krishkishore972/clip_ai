package preparation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var outputPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validArtifacts(c Claim, a *Artifacts) bool {
	if a == nil || a.Bucket != c.Bucket || !outputPattern.MatchString(c.OutputID) || c.OutputID == c.Token {
		return false
	}
	prefix := "dev-uploads/" + c.AssetID + "/derived/v1/" + c.OutputID + "/"
	return a.AudioKey == prefix+"audio.wav" &&
		hashPattern.MatchString(a.AudioSHA256) && hashPattern.MatchString(a.SourceSHA256) &&
		a.AudioSizeBytes > 0 && a.AudioSizeBytes <= 240_000_000 &&
		a.AudioStartMs >= -media.MaxAudioStartMs && a.AudioStartMs <= media.MaxAudioStartMs
}

func (s SQLStore) Finish(ctx context.Context, c Claim, o Outcome) error {
	if c.AssetID == "" || c.Token == "" {
		return ErrArguments
	}
	if o.Status == "ready" {
		if o.Code != "" || !validArtifacts(c, o.Artifacts) {
			return ErrArguments
		}
	} else if (o.Status != "pending" && o.Status != "failed") || o.Artifacts != nil || o.Code == "" {
		return ErrArguments
	}
	if s.DB == nil {
		return ErrDatabase
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return dbError(op)
	}
	defer tx.Rollback()
	var bucket, key string
	var duration int64
	err = tx.QueryRowContext(op, `
		SELECT
		    storage_bucket,
		    storage_key,
		    duration_ms
		FROM media_assets
		WHERE id = $1
		    AND status = 'ready'
		    AND user_id IS NOT NULL
		FOR UPDATE
	`, c.AssetID).Scan(&bucket, &key, &duration)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return dbError(op)
	}
	var attempts int
	var output string
	var live bool
	err = tx.QueryRowContext(op, `
		SELECT
		    attempts,
		    COALESCE(output_id, ''),
		    COALESCE(
		        status = 'preparing'
		        AND claim_token = $2
		        AND lease_until > clock_timestamp(),
		        false
		    )
		FROM media_preparations
		WHERE asset_id = $1
		FOR UPDATE
	`, c.AssetID, c.Token).Scan(&attempts, &output, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return dbError(op)
	}
	// Recheck time after acquiring the preparation lock too.
	if !live {
		return ErrStale
	}
	var current bool
	if err := tx.QueryRowContext(op, `
		SELECT lease_until > clock_timestamp()
		FROM media_preparations
		WHERE asset_id = $1
	`, c.AssetID).Scan(&current); err != nil {
		return dbError(op)
	}
	if !current || output != c.OutputID || bucket != c.Bucket || key != c.Key || duration != c.Metadata.DurationMs {
		return ErrStale
	}
	if o.Status == "ready" {
		a := o.Artifacts
		result, err := tx.ExecContext(op, `
		UPDATE media_preparations
		SET
		    status = 'ready',
		    failure_code = NULL,
		    claim_token = NULL,
		    lease_until = NULL,
		    output_id = NULL,
		    storage_bucket = $2,
		    audio_key = $3,
		    audio_sha256 = $4,
		    source_sha256 = $5,
		    audio_size_bytes = $6,
		    audio_start_ms = $7,
		    prepared_at = clock_timestamp(),
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
		    AND lease_until > clock_timestamp()
	`, c.AssetID, a.Bucket, a.AudioKey, a.AudioSHA256, a.SourceSHA256, a.AudioSizeBytes, a.AudioStartMs)
		if err != nil {
			return dbError(op)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return dbError(op)
		}
		if affected != 1 {
			return ErrStale
		}
	} else if err := failOrRetry(op, tx, c.AssetID, attempts, SafeFailureCode(o.Code), o.Status == "pending"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbError(op)
	}
	return nil
}

func failOrRetry(ctx context.Context, tx *sql.Tx, id string, attempts int, code string, retry bool) error {
	status := "failed"
	if retry && attempts < MaxAttempts {
		status = "pending"
	}
	if retry && attempts >= MaxAttempts {
		code = "PREPARATION_ATTEMPTS_EXHAUSTED"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE media_preparations
		SET
		    status = $2,
		    failure_code = $3,
		    claim_token = NULL,
		    lease_until = NULL,
		    output_id = NULL,
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
	`, id, status, code); err != nil {
		return dbError(ctx)
	}
	if status == "failed" {
		return nil
	}
	event, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
		    id,
		    asset_id,
		    task_type,
		    queue_name,
		    payload,
		    dedupe_key,
		    next_attempt_at
		)
		VALUES (
		    $1,
		    $2,
		    'media:process',
		    'processing',
		    jsonb_build_object('assetId', $2::text),
		    $3,
		    clock_timestamp() + INTERVAL '1 minute'
		)
		ON CONFLICT (dedupe_key) DO NOTHING
	`, event, id, fmt.Sprintf("prepare:%s:v1:retry:%d", id, attempts))
	if err != nil {
		return dbError(ctx)
	}
	return nil
}

func (s SQLStore) Recover(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, ErrArguments
	}
	if s.DB == nil {
		return 0, ErrDatabase
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return 0, dbError(op)
	}
	defer tx.Rollback()
	// Always lock assets before preparations, including recovery/archival paths.
	rows, err := tx.QueryContext(op, `
		SELECT a.id
		FROM media_assets a
		WHERE a.status = 'ready'
		    AND a.user_id IS NOT NULL
		    AND EXISTS (
		        SELECT 1
		        FROM media_preparations p
		        WHERE p.asset_id = a.id
		            AND p.status = 'preparing'
		            AND p.lease_until <= clock_timestamp()
		    )
		ORDER BY a.id
		LIMIT $1
		FOR UPDATE OF a SKIP LOCKED
	`, min(limit, 100))
	if err != nil {
		return 0, dbError(op)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, dbError(op)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, dbError(op)
	}
	n := 0
	for _, id := range ids {
		var attempts int
		err := tx.QueryRowContext(op, `
		SELECT attempts
		FROM media_preparations
		WHERE asset_id = $1
		    AND status = 'preparing'
		    AND lease_until <= clock_timestamp()
		FOR UPDATE
	`, id).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, dbError(op)
		}
		if err := failOrRetry(op, tx, id, attempts, "PREPARATION_INTERRUPTED", true); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, dbError(op)
	}
	return n, nil
}
