package upload

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/krishkishore972/clip_ai/internal/auth"
)

type CompletedAsset struct {
	ID, Bucket, Key, Filename, ContentType string
	SizeBytes                              int64
	ClipPreferences                        *ClipPreferences
}

type AssetRecorder interface {
	SaveCompleted(context.Context, CompletedAsset) error
}

type SQLAssets struct {
	DB *sql.DB
}

// SaveCompleted atomically records the completed asset and its initial
// validation event, plus signed clip preferences when present. Retries preserve
// existing ownership, media metadata, preferences and curation execution state.
func (s SQLAssets) SaveCompleted(
	ctx context.Context,
	asset CompletedAsset,
) error {
	principal, ok := auth.FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if asset.ClipPreferences != nil && !asset.ClipPreferences.Valid() {
		return ErrInvalid
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin completed asset transaction: %w", err)
	}
	defer tx.Rollback()

	const saveAssetQuery = `
		INSERT INTO media_assets (
			id,
			user_id,
			storage_bucket,
			storage_key,
			original_filename,
			declared_content_type,
			size_bytes
		)
		SELECT
			$1,
			$2,
			$3,
			$4,
			$5,
			$6,
			$7
		WHERE EXISTS (
			SELECT 1
			FROM users
			WHERE id = $2
		)
		ON CONFLICT (id) DO UPDATE
		SET id = EXCLUDED.id
		WHERE media_assets.user_id = EXCLUDED.user_id
			AND media_assets.storage_bucket = EXCLUDED.storage_bucket
			AND media_assets.storage_key = EXCLUDED.storage_key
			AND media_assets.original_filename = EXCLUDED.original_filename
			AND media_assets.declared_content_type = EXCLUDED.declared_content_type
			AND media_assets.size_bytes = EXCLUDED.size_bytes
		RETURNING id
	`

	var assetID string

	err = tx.QueryRowContext(
		ctx,
		saveAssetQuery,
		asset.ID,
		principal.UserID,
		asset.Bucket,
		asset.Key,
		asset.Filename,
		asset.ContentType,
		asset.SizeBytes,
	).Scan(&assetID)

	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("persist completed asset: %w", err)
	}

	// Legacy capabilities have no preferences. Do not invent them or backfill
	// paid curation work for those uploads.
	if preferences := asset.ClipPreferences; preferences != nil {
		const saveCurationQuery = `
			INSERT INTO media_curation_jobs (
			    asset_id,
			    candidate_count,
			    min_duration_ms,
			    max_duration_ms
			)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (asset_id) DO UPDATE
			SET asset_id = EXCLUDED.asset_id
			WHERE media_curation_jobs.candidate_count = EXCLUDED.candidate_count
			    AND media_curation_jobs.min_duration_ms = EXCLUDED.min_duration_ms
			    AND media_curation_jobs.max_duration_ms = EXCLUDED.max_duration_ms
			RETURNING asset_id
		`
		var curationAssetID string
		err := tx.QueryRowContext(
			ctx,
			saveCurationQuery,
			assetID,
			preferences.CandidateCount,
			preferences.MinDurationMs,
			preferences.MaxDurationMs,
		).Scan(&curationAssetID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("persist curation preferences: %w", err)
		}
	}

	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return fmt.Errorf("generate outbox event ID: %w", err)
	}

	eventID := hex.EncodeToString(randomID[:])
	dedupeKey := "validate:" + assetID + ":initial"

	const insertEventQuery = `
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
			'media:validate',
			'validation',
			jsonb_build_object('assetId', $2::text),
			$3
		)
		ON CONFLICT (dedupe_key) DO NOTHING
	`

	_, err = tx.ExecContext(
		ctx,
		insertEventQuery,
		eventID,
		assetID,
		dedupeKey,
	)
	if err != nil {
		return fmt.Errorf("persist validation outbox event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completed asset transaction: %w", err)
	}

	return nil
}
