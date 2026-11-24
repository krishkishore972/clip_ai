// Package curation provides persistence for server-side clip proposals.
package curation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
)

const (
	TaskType           = "media:curate"
	Queue              = "curation"
	MaxTranscriptWords = 12_000
)

var (
	ErrConfiguration = errors.New("curation: invalid handoff configuration")
	ErrDatabase      = errors.New("curation: handoff database operation failed")
	ErrConflict      = errors.New("curation: existing transcript handoff conflicts")
	ErrNoTranscript  = errors.New("curation: asset has no ready transcript")
	ErrNoPreferences = errors.New("curation: asset has no clip preferences")
	assetIdentifier  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type transcriptSnapshot struct {
	bucket, key, sha256         string
	sizeBytes, sourceDurationMs int64
	wordCount                   int
}

// EnqueueReadyTranscript joins the caller's transaction: it never commits it.
// Call after publishing media_transcript_files.ready in that same transaction.
// Lock order is transcript, then curation job. A missing ready transcript or a
// missing preferences row is an invariant violation, not a no-op. Already
// started or terminal jobs produce no queue work.
//
// The boolean reports a newly inserted outbox event, not Redis delivery.
// Oversized transcripts fail only the pending curation job, without an event.
// This does not read S3, validate document content or call the LLM service.
func EnqueueReadyTranscript(ctx context.Context, tx *sql.Tx, assetID string) (bool, error) {
	if tx == nil || !assetIdentifier.MatchString(assetID) {
		return false, ErrConfiguration
	}

	var source transcriptSnapshot
	err := tx.QueryRowContext(ctx, `
		SELECT
		    t.storage_bucket,
		    t.transcript_key,
		    t.transcript_sha256,
		    t.transcript_size_bytes,
		    t.word_count,
		    t.source_duration_ms
		FROM media_transcript_files t
		JOIN media_assets a ON a.id = t.asset_id
		WHERE t.asset_id = $1
		    AND t.status = 'ready'
		    AND a.user_id IS NOT NULL
		    AND a.status = 'ready'
		    AND a.duration_ms = t.source_duration_ms
		FOR UPDATE OF t
	`, assetID).Scan(
		&source.bucket,
		&source.key,
		&source.sha256,
		&source.sizeBytes,
		&source.wordCount,
		&source.sourceDurationMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNoTranscript
	}
	if err != nil {
		return false, ErrDatabase
	}

	var status string
	var attempts int
	var bucket, key, hash sql.NullString
	var size, words, duration sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT
		    status,
		    attempts,
		    transcript_bucket,
		    transcript_key,
		    transcript_sha256,
		    transcript_size_bytes,
		    transcript_word_count,
		    source_duration_ms
		FROM media_curation_jobs
		WHERE asset_id = $1
		FOR UPDATE
	`, assetID).Scan(
		&status, &attempts, &bucket, &key, &hash, &size, &words, &duration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNoPreferences
	}
	if err != nil {
		return false, ErrDatabase
	}
	if status != "pending" || attempts != 0 {
		return false, nil
	}
	if hash.Valid && (bucket.String != source.bucket ||
		key.String != source.key ||
		hash.String != source.sha256 ||
		size.Int64 != source.sizeBytes ||
		words.Int64 != int64(source.wordCount) ||
		duration.Int64 != source.sourceDurationMs) {
		// Never silently replace an already-bound input.
		return false, ErrConflict
	}

	if source.wordCount > MaxTranscriptWords {
		// The transcript itself remains valid/ready. Its word count cannot fit
		// the curation snapshot constraint, so leave that snapshot empty.
		_, err := tx.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET
			    status = 'failed',
			    failure_code = 'CURATION_INPUT_TOO_LARGE',
			    completed_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE asset_id = $1
		`, assetID)
		if err != nil {
			return false, ErrDatabase
		}
		return false, nil
	}

	if !hash.Valid {
		_, err := tx.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET
			    transcript_bucket = $2,
			    transcript_key = $3,
			    transcript_sha256 = $4,
			    transcript_size_bytes = $5,
			    transcript_word_count = $6,
			    source_duration_ms = $7,
			    failure_code = NULL,
			    next_attempt_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE asset_id = $1
		`, assetID, source.bucket, source.key, source.sha256,
			source.sizeBytes, source.wordCount, source.sourceDurationMs)
		if err != nil {
			return false, ErrDatabase
		}
	}

	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return false, ErrDatabase
	}
	dedupe := "curate:" + assetID + ":" + source.sha256 + ":initial"
	result, err := tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
		    id,
		    asset_id,
		    task_type,
		    queue_name,
		    payload,
		    dedupe_key
		)
		VALUES (
		    $1,
		    $2,
		    $3,
		    $4,
		    jsonb_build_object('assetId', $2::text),
		    $5
		)
		ON CONFLICT (dedupe_key) DO NOTHING
	`, hex.EncodeToString(randomID[:]), assetID, TaskType, Queue, dedupe)
	if err != nil {
		return false, ErrDatabase
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, ErrDatabase
	}
	if n == 0 {
		var same bool
		err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
			    SELECT 1
			    FROM outbox_events
			    WHERE dedupe_key = $1
			        AND asset_id = $2
			        AND task_type = $3
			        AND queue_name = $4
			        AND payload = jsonb_build_object('assetId', $2::text)
			)
		`, dedupe, assetID, TaskType, Queue).Scan(&same)
		if err != nil {
			return false, ErrDatabase
		}
		if !same {
			return false, ErrConflict
		}
	}
	return n == 1, nil
}
