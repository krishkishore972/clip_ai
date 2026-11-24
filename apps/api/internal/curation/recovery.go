package curation

import (
	"context"
	"fmt"
)

// Recover repairs expired claims and bound pending jobs whose delivery may have
// been lost. It never backfills jobs still waiting for a transcript snapshot.
// Each repair is its own bounded transaction and rechecks eligibility after
// locking transcript then curation. Concurrent recoverers cannot double-repair.
func (s SQLStore) Recover(ctx context.Context, limit int) (int, error) {
	if s.DB == nil || limit < 1 || limit > 100 {
		return 0, ErrConfiguration
	}
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	rows, err := s.DB.QueryContext(op, `
		SELECT asset_id
		FROM media_curation_jobs
		WHERE (status = 'running' AND lease_until <= clock_timestamp())
		    OR (
		        status = 'pending'
		        AND transcript_sha256 IS NOT NULL
		        AND next_attempt_at <= clock_timestamp()
		        AND updated_at <= clock_timestamp() - $1 * INTERVAL '1 microsecond'
		    )
		ORDER BY asset_id
		LIMIT $2
	`, PendingRecovery.Microseconds(), limit)
	if err != nil {
		return 0, storeError(op)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, storeError(op)
		}
		ids = append(ids, id)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil || closeErr != nil {
		return 0, storeError(op)
	}
	repaired := 0
	for _, id := range ids {
		ok, err := s.recoverOne(op, id)
		if err != nil {
			// Earlier individual repairs may already be committed.
			return repaired, err
		}
		if ok {
			repaired++
		}
	}
	return repaired, nil
}

func (s SQLStore) recoverOne(ctx context.Context, id string) (bool, error) {
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return false, storeError(op)
	}
	defer tx.Rollback()
	j, err := lockJob(op, tx, id, true)
	if err != nil {
		return false, err
	}
	if j.claim.AssetID == "" {
		return false, nil
	}
	var due bool
	var generation int64
	if err := tx.QueryRowContext(op, `
		SELECT
		    (status = 'running' AND lease_until <= clock_timestamp())
		    OR (
		        status = 'pending' AND transcript_sha256 IS NOT NULL
		        AND next_attempt_at <= clock_timestamp()
		        AND updated_at <= clock_timestamp() - $2 * INTERVAL '1 microsecond'
		    ),
		    recovery_generation
		FROM media_curation_jobs
		WHERE asset_id = $1
	`, id, PendingRecovery.Microseconds()).Scan(&due, &generation); err != nil {
		return false, storeError(op)
	}
	if !due {
		return false, nil
	}
	code := "CURATION_INTERRUPTED"
	retry := j.claim.Attempts < MaxAttempts
	// Pending pre-claim jobs have no token/attempt yet; validate their snapshot
	// with a synthetic next-claim envelope, without consuming that attempt.
	check := j.claim
	check.Token, check.Attempts = "recovery-check", 1
	if !j.eligible || !check.valid() {
		code, retry = "CURATION_SOURCE_CHANGED", false
	} else if !retry {
		code = "CURATION_RETRIES_EXHAUSTED"
	}
	if err := finishFailure(op, tx, id, code, retry); err != nil {
		return false, err
	}
	if retry {
		if _, err := tx.ExecContext(op, `
			UPDATE media_curation_jobs
			SET recovery_generation = recovery_generation + 1
			WHERE asset_id = $1
		`, id); err != nil {
			return false, storeError(op)
		}
		dedupe := fmt.Sprintf("curate:%s:%s:recover:%d", id, j.claim.TranscriptSHA256, generation+1)
		if err := recoveryEvent(op, tx, id, dedupe); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, storeError(op)
	}
	return true, nil
}
