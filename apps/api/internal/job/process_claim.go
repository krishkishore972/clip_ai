package job

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Claim reserves both validation and preparation in one short transaction.
// All worker transitions lock the asset before its preparation. No SQL locks
// are held while downloading or processing media.
func (s SQLProcessStore) Claim(ctx context.Context, id, token string, lease time.Duration) (ProcessAsset, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(token) == "" ||
		lease < time.Microsecond || s.DB == nil {
		return ProcessAsset{}, ErrProcessArguments
	}
	op, cancel := context.WithTimeout(ctx, processDatabaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	defer tx.Rollback()

	const assetQuery = `
		SELECT
		    id,
		    storage_bucket,
		    storage_key,
		    size_bytes,
		    status,
		    COALESCE(validation_lease_until > clock_timestamp(), FALSE)
		FROM media_assets
		WHERE id = $1
		    AND user_id IS NOT NULL
		FOR UPDATE
	`
	var asset ProcessAsset
	var assetStatus string
	var validationLive bool
	err = tx.QueryRowContext(op, assetQuery, id).Scan(
		&asset.ID,
		&asset.Bucket,
		&asset.Key,
		&asset.SizeBytes,
		&assetStatus,
		&validationLive,
	)
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

	const insertPreparation = `
		INSERT INTO media_preparations (asset_id)
		VALUES ($1)
		ON CONFLICT (asset_id) DO NOTHING
	`
	if _, err := tx.ExecContext(op, insertPreparation, id); err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	const preparationQuery = `
		SELECT
		    status,
		    attempts,
		    COALESCE(
		        status = 'preparing' AND lease_until > clock_timestamp(),
		        FALSE
		    )
		FROM media_preparations
		WHERE asset_id = $1
		FOR UPDATE
	`
	var prepStatus string
	var prepLive bool
	err = tx.QueryRowContext(op, preparationQuery, id).Scan(
		&prepStatus,
		&asset.Attempts,
		&prepLive,
	)
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
		if err := saveProcessFailure(op, tx, id, "validation_failed", "PREPARATION_ATTEMPTS_EXHAUSTED"); err != nil {
			return ProcessAsset{}, err
		}
		if err := tx.Commit(); err != nil {
			return ProcessAsset{}, processDBError(ctx, op)
		}
		return ProcessAsset{}, nil
	}

	// Output namespaces must not reveal the fencing token.
	for asset.OutputID == "" || asset.OutputID == token {
		var output [16]byte
		if _, err := rand.Read(output[:]); err != nil {
			return ProcessAsset{}, ErrProcessDatabase
		}
		asset.OutputID = hex.EncodeToString(output[:])
	}
	asset.Token = token
	asset.Attempts++

	const claimAsset = `
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
	if _, err := tx.ExecContext(op, claimAsset, id, token, lease.Microseconds()); err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	const claimPreparation = `
		UPDATE media_preparations
		SET
		    status = 'preparing',
		    attempts = attempts + 1,
		    claim_token = $2,
		    output_id = $3,
		    lease_until = clock_timestamp() + $4 * INTERVAL '1 microsecond',
		    failure_code = NULL,
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
	`
	_, err = tx.ExecContext(
		op,
		claimPreparation,
		id,
		token,
		asset.OutputID,
		lease.Microseconds(),
	)
	if err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	if err := tx.Commit(); err != nil {
		return ProcessAsset{}, processDBError(ctx, op)
	}
	return asset, nil
}
