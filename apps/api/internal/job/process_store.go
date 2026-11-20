package job

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

const (
	ProcessTaskType = "media:process"
	ProcessQueue    = "processing"
	ProcessMaxRetry = 1
	ProcessAttempts = 2
)

var (
	ErrProcessDatabase  = errors.New("media process database operation failed")
	ErrProcessBusy      = errors.New("media process claim is busy")
	ErrProcessStale     = errors.New("media process claim is stale")
	ErrProcessArguments = errors.New("invalid media process arguments")
)

type ProcessAsset struct {
	ID, Bucket, Key, Token, OutputID string
	SizeBytes                        int64
	Attempts                         int
}

type ProcessOutcome struct {
	AssetStatus string
	PrepStatus  string
	Code        string
	Metadata    *media.Metadata
	Artifacts   *preparation.Artifacts
}

type ProcessStore interface {
	Claim(context.Context, string, string, time.Duration) (ProcessAsset, error)
	Finish(context.Context, ProcessAsset, ProcessOutcome) error
	Recover(context.Context, int) (int, error)
}

type SQLProcessStore struct{ DB *sql.DB }

func processDBError(parent, operation context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := operation.Err(); err != nil {
		return err
	}
	return ErrProcessDatabase
}

func processEvent(ctx context.Context, tx *sql.Tx, assetID, dedupe string, next time.Duration) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ErrProcessDatabase
	}
	payload, err := json.Marshal(struct {
		AssetID string `json:"assetId"`
	}{assetID})
	if err != nil {
		return ErrProcessDatabase
	}
	query := `
		INSERT INTO outbox_events (
		    id, asset_id, task_type, queue_name, payload, dedupe_key, next_attempt_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp() + $7 * INTERVAL '1 microsecond')
		ON CONFLICT (dedupe_key) DO NOTHING
	`
	if _, err := tx.ExecContext(ctx, query, hex.EncodeToString(id[:]), assetID, ProcessTaskType, ProcessQueue, payload, dedupe, next.Microseconds()); err != nil {
		return processDBError(ctx, ctx)
	}
	return nil
}

// Claim reserves both validation and preparation in one short transaction.
// The worker then downloads and processes the source without holding locks.
func (s SQLProcessStore) Claim(ctx context.Context, id, token string, lease time.Duration) (ProcessAsset, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(token) == "" || lease < time.Microsecond || s.DB == nil {
		return ProcessAsset{}, ErrProcessArguments
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	defer tx.Rollback()

	var asset ProcessAsset
	var assetStatus string
	var validationLive bool
	err = tx.QueryRowContext(op, `
		SELECT id, storage_bucket, storage_key, size_bytes, status,
		       COALESCE(validation_lease_until > clock_timestamp(), FALSE)
		FROM media_assets
		WHERE id = $1 AND user_id IS NOT NULL
		FOR UPDATE
	`, id).Scan(&asset.ID, &asset.Bucket, &asset.Key, &asset.SizeBytes, &assetStatus, &validationLive)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcessAsset{}, nil
	}
	if err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	if assetStatus != "uploaded_unvalidated" && assetStatus != "validating" && assetStatus != "ready" {
		return ProcessAsset{}, nil
	}
	if assetStatus == "validating" && validationLive {
		return ProcessAsset{}, ErrProcessBusy
	}

	_, err = tx.ExecContext(op, `
		INSERT INTO media_preparations (asset_id)
		VALUES ($1)
		ON CONFLICT (asset_id) DO NOTHING
	`, id)
	if err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	var prepStatus string
	var prepLive bool
	err = tx.QueryRowContext(op, `
		SELECT status, attempts, status = 'preparing' AND lease_until > clock_timestamp()
		FROM media_preparations
		WHERE asset_id = $1
		FOR UPDATE
	`, id).Scan(&prepStatus, &asset.Attempts, &prepLive)
	if err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	if prepStatus == "ready" || prepStatus == "failed" {
		return ProcessAsset{}, nil
	}
	if prepStatus == "preparing" && prepLive {
		return ProcessAsset{}, ErrProcessBusy
	}
	if asset.Attempts >= ProcessAttempts {
		if _, err := tx.ExecContext(op, `
			UPDATE media_preparations
			SET status = 'failed', failure_code = 'PREPARATION_ATTEMPTS_EXHAUSTED', updated_at = clock_timestamp()
			WHERE asset_id = $1
		`, id); err != nil {
			return ProcessAsset{}, processDBError(ctx, op)
		}
		if err := tx.Commit(); err != nil {
			return ProcessAsset{}, processDBError(ctx, op)
		}
		return ProcessAsset{}, nil
	}

	var output [16]byte
	if _, err := rand.Read(output[:]); err != nil {
		return ProcessAsset{}, ErrProcessDatabase
	}
	asset.Token = token
	asset.OutputID = hex.EncodeToString(output[:])
	asset.Attempts++
	if _, err := tx.ExecContext(op, `
		UPDATE media_assets
		SET status = 'validating', validation_attempts = validation_attempts + 1,
		    validation_claim_token = $2,
		    validation_lease_until = clock_timestamp() + $3 * INTERVAL '1 microsecond',
		    failure_code = NULL, updated_at = clock_timestamp()
		WHERE id = $1
	`, id, token, lease.Microseconds()); err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	if _, err := tx.ExecContext(op, `
		UPDATE media_preparations
		SET status = 'preparing', attempts = attempts + 1,
		    claim_token = $2, output_id = $3,
		    lease_until = clock_timestamp() + $4 * INTERVAL '1 microsecond',
		    failure_code = NULL, updated_at = clock_timestamp()
		WHERE asset_id = $1
	`, id, token, asset.OutputID, lease.Microseconds()); err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	if err := tx.Commit(); err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	return asset, nil
}

func (s SQLProcessStore) Finish(ctx context.Context, asset ProcessAsset, outcome ProcessOutcome) error {
	if asset.ID == "" || asset.Token == "" || asset.OutputID == "" || s.DB == nil {
		return ErrProcessArguments
	}
	if outcome.AssetStatus == "ready" {
		if outcome.PrepStatus != "ready" || outcome.Metadata == nil || outcome.Artifacts == nil {
			return ErrProcessArguments
		}
	} else if outcome.AssetStatus != "rejected" && outcome.AssetStatus != "uploaded_unvalidated" && outcome.AssetStatus != "validation_failed" {
		return ErrProcessArguments
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return processDBError(ctx, op)
	}
	defer tx.Rollback()
	var bucket, key string
	err = tx.QueryRowContext(op, `
		SELECT storage_bucket, storage_key FROM media_assets WHERE id = $1 FOR UPDATE
	`, asset.ID).Scan(&bucket, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProcessStale
	}
	if err != nil {
		return processDBError(ctx, op)
	}
	var prepAttempts int
	var prepOutput string
	var live bool
	err = tx.QueryRowContext(op, `
		SELECT attempts, COALESCE(output_id, ''),
		       status = 'preparing' AND claim_token = $2 AND lease_until > clock_timestamp()
		FROM media_preparations WHERE asset_id = $1 FOR UPDATE
	`, asset.ID, asset.Token).Scan(&prepAttempts, &prepOutput, &live)
	if errors.Is(err, sql.ErrNoRows) || !live || prepOutput != asset.OutputID || bucket != asset.Bucket || key != asset.Key {
		return ErrProcessStale
	}
	if outcome.AssetStatus == "ready" {
		m, err := json.Marshal(outcome.Metadata)
		if err != nil {
			return ErrProcessArguments
		}
		v, a := outcome.Metadata.Video[0], outcome.Metadata.Audio[0]
		result, err := tx.ExecContext(op, `
			UPDATE media_assets
			SET status = 'ready', failure_code = NULL, probed_metadata = $3,
			    duration_ms = $4, width = $5, height = $6, fps_num = $7, fps_den = $8,
			    video_codec = $9, audio_codec = $10, validated_at = clock_timestamp(),
			    validation_claim_token = NULL, validation_lease_until = NULL, updated_at = clock_timestamp()
			WHERE id = $1 AND validation_claim_token = $2 AND status = 'validating'
			  AND validation_lease_until > clock_timestamp() AND user_id IS NOT NULL
		`, asset.ID, asset.Token, m, outcome.Metadata.DurationMs, v.Width, v.Height, v.FPS.Num, v.FPS.Den, v.Codec, a.Codec)
		if err != nil {
			return processDBError(ctx, op)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return ErrProcessStale
		}
		artifacts := outcome.Artifacts
		result, err = tx.ExecContext(op, `
			UPDATE media_preparations
			SET status = 'ready', failure_code = NULL, claim_token = NULL, lease_until = NULL, output_id = NULL,
			    storage_bucket = $2, audio_key = $3, audio_sha256 = $4, source_sha256 = $5,
			    audio_size_bytes = $6, audio_start_ms = $7,
			    prepared_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE asset_id = $1 AND lease_until > clock_timestamp()
		`, asset.ID, artifacts.Bucket, artifacts.AudioKey, artifacts.AudioSHA256, artifacts.SourceSHA256, artifacts.AudioSizeBytes, artifacts.AudioStartMs)
		if err != nil {
			return processDBError(ctx, op)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return ErrProcessStale
		}
	} else {
		if outcome.Code == "" || !processCodePattern.MatchString(outcome.Code) {
			return ErrProcessArguments
		}
		if _, err := tx.ExecContext(op, `
			UPDATE media_assets
			SET status = $3, failure_code = $4, validation_claim_token = NULL,
			    validation_lease_until = NULL, updated_at = clock_timestamp()
			WHERE id = $1 AND validation_claim_token = $2 AND status = 'validating'
		`, asset.ID, asset.Token, outcome.AssetStatus, outcome.Code); err != nil {
			return processDBError(ctx, op)
		}
		prepStatus := "failed"
		if outcome.AssetStatus == "uploaded_unvalidated" {
			prepStatus = "pending"
		}
		if _, err := tx.ExecContext(op, `
			UPDATE media_preparations
			SET status = $2, failure_code = $3, claim_token = NULL, lease_until = NULL, output_id = NULL,
			    updated_at = clock_timestamp()
			WHERE asset_id = $1
		`, asset.ID, prepStatus, preparation.SafeFailureCode(outcome.Code)); err != nil {
			return processDBError(ctx, op)
		}
		if prepStatus == "pending" {
			if err := processEvent(op, tx, asset.ID, fmt.Sprintf("process:%s:retry:%d", asset.ID, prepAttempts), time.Minute); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return processDBError(ctx, op)
	}
	return nil
}

func (s SQLProcessStore) Recover(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || s.DB == nil {
		return 0, ErrProcessArguments
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return 0, processDBError(ctx, op)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(op, `
		SELECT a.id, p.attempts
		FROM media_assets a JOIN media_preparations p ON p.asset_id = a.id
		WHERE p.status = 'preparing' AND p.lease_until <= clock_timestamp()
		ORDER BY p.lease_until, a.id LIMIT $1 FOR UPDATE OF a, p SKIP LOCKED
	`, min(limit, 100))
	if err != nil {
		return 0, processDBError(ctx, op)
	}
	type item struct {
		id       string
		attempts int
	}
	var items []item
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.attempts); err != nil {
			rows.Close()
			return 0, processDBError(ctx, op)
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, processDBError(ctx, op)
	}
	for _, item := range items {
		status, code := "pending", "PREPARATION_INTERRUPTED"
		if item.attempts >= ProcessAttempts {
			status, code = "failed", "PREPARATION_ATTEMPTS_EXHAUSTED"
		}
		if _, err := tx.ExecContext(op, `
			UPDATE media_assets
			SET status = CASE WHEN $2 = 'pending' THEN 'uploaded_unvalidated' ELSE 'validation_failed' END,
			    failure_code = $3, validation_claim_token = NULL, validation_lease_until = NULL,
			    updated_at = clock_timestamp() WHERE id = $1
		`, item.id, status, code); err != nil {
			return 0, processDBError(ctx, op)
		}
		if _, err := tx.ExecContext(op, `
			UPDATE media_preparations
			SET status = $2, failure_code = $3, claim_token = NULL, lease_until = NULL, output_id = NULL,
			    updated_at = clock_timestamp() WHERE asset_id = $1
		`, item.id, status, preparation.SafeFailureCode(code)); err != nil {
			return 0, processDBError(ctx, op)
		}
		if status == "pending" {
			if err := processEvent(op, tx, item.id, fmt.Sprintf("process:%s:recovery:%d", item.id, item.attempts), 0); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, processDBError(ctx, op)
	}
	return len(items), nil
}
