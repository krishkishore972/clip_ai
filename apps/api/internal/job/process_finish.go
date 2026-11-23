package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func validateProcessOutcome(asset ProcessAsset, outcome ProcessOutcome) error {
	if asset.ID == "" || asset.Token == "" || asset.OutputID == "" || asset.OutputID == asset.Token {
		return ErrProcessArguments
	}
	if outcome.AssetStatus == "ready" {
		if outcome.PrepStatus != "ready" || outcome.Code != "" ||
			!validStoredMetadata(outcome.Metadata) ||
			outcome.Metadata.SizeBytes != asset.SizeBytes {
			return ErrProcessArguments
		}
		if err := outcome.Artifacts.Validate(asset.ID, asset.Bucket, asset.OutputID); err != nil {
			return ErrProcessArguments
		}
		return nil
	}
	switch outcome.AssetStatus {
	case "rejected", "uploaded_unvalidated", "validation_failed":
	default:
		return ErrProcessArguments
	}
	if outcome.Metadata != nil || outcome.Artifacts != nil ||
		!processCodePattern.MatchString(outcome.Code) {
		return ErrProcessArguments
	}
	return nil
}

func (s SQLProcessStore) Finish(ctx context.Context, asset ProcessAsset, outcome ProcessOutcome) error {
	if err := validateProcessOutcome(asset, outcome); err != nil {
		return err
	}
	if s.DB == nil {
		return ErrProcessArguments
	}
	op, cancel := context.WithTimeout(ctx, processDatabaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return processDBError(ctx, op)
	}
	defer tx.Rollback()

	const assetQuery = `
		SELECT
		    storage_bucket,
		    storage_key,
		    COALESCE(
		        status = 'validating'
		        AND validation_claim_token = $2
		        AND validation_lease_until > clock_timestamp(),
		        FALSE
		    )
		FROM media_assets
		WHERE id = $1
		FOR UPDATE
	`
	var bucket, key string
	var assetLive bool
	err = tx.QueryRowContext(op, assetQuery, asset.ID, asset.Token).Scan(&bucket, &key, &assetLive)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProcessStale
	}
	if err != nil {
		return processDBError(ctx, op)
	}

	const preparationQuery = `
		SELECT
		    attempts,
		    COALESCE(output_id, ''),
		    COALESCE(
		        status = 'preparing'
		        AND claim_token = $2
		        AND lease_until > clock_timestamp(),
		        FALSE
		    )
		FROM media_preparations
		WHERE asset_id = $1
		FOR UPDATE
	`
	var attempts int
	var outputID string
	var live bool
	err = tx.QueryRowContext(op, preparationQuery, asset.ID, asset.Token).Scan(
		&attempts,
		&outputID,
		&live,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProcessStale
	}
	if err != nil {
		return processDBError(ctx, op)
	}
	if !assetLive || !live || outputID != asset.OutputID ||
		bucket != asset.Bucket || key != asset.Key {
		return ErrProcessStale
	}

	if outcome.AssetStatus == "ready" {
		if err := saveReadyProcess(op, tx, asset, outcome); err != nil {
			return err
		}
		if s.TranscriptionLanguage != "" {
			if err := transcription.InsertFileJob(op, tx, asset.ID, s.TranscriptionLanguage); err != nil {
				return err
			}
		}
	} else {
		if err := saveProcessFailure(op, tx, asset.ID, outcome.AssetStatus, outcome.Code); err != nil {
			return err
		}
		if outcome.AssetStatus == "uploaded_unvalidated" {
			dedupe := fmt.Sprintf("process:%s:retry:%d", asset.ID, attempts)
			if err := processEvent(op, tx, asset.ID, dedupe, time.Minute); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return processDBError(ctx, op)
	}
	return nil
}

// saveReadyProcess publishes both outcomes under the locks held by Finish.
func saveReadyProcess(ctx context.Context, tx *sql.Tx, asset ProcessAsset, outcome ProcessOutcome) error {
	metadata := outcome.Metadata
	raw, err := json.Marshal(metadata)
	if err != nil {
		return ErrProcessArguments
	}
	video, audio := metadata.Video[0], metadata.Audio[0]
	const saveAsset = `
		UPDATE media_assets
		SET
		    status = 'ready',
		    failure_code = NULL,
		    probed_metadata = $3,
		    duration_ms = $4,
		    width = $5,
		    height = $6,
		    fps_num = $7,
		    fps_den = $8,
		    video_codec = $9,
		    audio_codec = $10,
		    validated_at = clock_timestamp(),
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
		ctx,
		saveAsset,
		asset.ID,
		asset.Token,
		raw,
		metadata.DurationMs,
		video.Width,
		video.Height,
		video.FPS.Num,
		video.FPS.Den,
		video.Codec,
		audio.Codec,
	)
	if err := requireProcessUpdate(ctx, result, err); err != nil {
		return err
	}

	artifacts := outcome.Artifacts
	const savePreparation = `
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
	`
	result, err = tx.ExecContext(
		ctx,
		savePreparation,
		asset.ID,
		artifacts.Bucket,
		artifacts.AudioKey,
		artifacts.AudioSHA256,
		artifacts.SourceSHA256,
		artifacts.AudioSizeBytes,
		artifacts.AudioStartMs,
	)
	return requireProcessUpdate(ctx, result, err)
}

// saveProcessFailure releases both claims consistently. Callers must hold the
// asset and preparation row locks and validate any worker fencing token first.
// Only Finish and Recover schedule retry events, in the same transaction.
func saveProcessFailure(ctx context.Context, tx *sql.Tx, assetID, assetStatus, code string) error {
	prepStatus := "failed"
	if assetStatus == "uploaded_unvalidated" {
		prepStatus = "pending"
	}
	const saveAsset = `
		UPDATE media_assets
		SET
		    status = $2,
		    failure_code = $3,
		    validation_claim_token = NULL,
		    validation_lease_until = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1
	`
	result, err := tx.ExecContext(ctx, saveAsset, assetID, assetStatus, code)
	if err := requireProcessUpdate(ctx, result, err); err != nil {
		return err
	}
	const savePreparation = `
		UPDATE media_preparations
		SET
		    status = $2,
		    failure_code = $3,
		    claim_token = NULL,
		    lease_until = NULL,
		    output_id = NULL,
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
	`
	result, err = tx.ExecContext(
		ctx,
		savePreparation,
		assetID,
		prepStatus,
		preparation.SafeFailureCode(code),
	)
	return requireProcessUpdate(ctx, result, err)
}

func requireProcessUpdate(ctx context.Context, result sql.Result, err error) error {
	if err != nil {
		return processDBError(ctx, ctx)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return processDBError(ctx, ctx)
	}
	if affected != 1 {
		return ErrProcessStale
	}
	return nil
}
