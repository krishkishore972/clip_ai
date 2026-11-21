package job

import (
	"context"
	"fmt"
)

func (s SQLProcessStore) Recover(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || s.DB == nil {
		return 0, ErrProcessArguments
	}
	op, cancel := context.WithTimeout(ctx, processDatabaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return 0, processDBError(ctx, op)
	}
	defer tx.Rollback()

	const expiredQuery = `
		SELECT
		    a.id,
		    p.attempts
		FROM media_assets a
		JOIN media_preparations p ON p.asset_id = a.id
		WHERE p.status = 'preparing'
		    AND p.lease_until <= clock_timestamp()
		ORDER BY p.lease_until, a.id
		LIMIT $1
		FOR UPDATE OF a, p SKIP LOCKED
	`
	rows, err := tx.QueryContext(op, expiredQuery, min(limit, 100))
	if err != nil {
		return 0, processDBError(ctx, op)
	}
	type expiredClaim struct {
		id       string
		attempts int
	}
	var claims []expiredClaim
	for rows.Next() {
		var claim expiredClaim
		if err := rows.Scan(&claim.id, &claim.attempts); err != nil {
			rows.Close()
			return 0, processDBError(ctx, op)
		}
		claims = append(claims, claim)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return 0, processDBError(ctx, op)
	}

	for _, claim := range claims {
		assetStatus, code := "uploaded_unvalidated", "PREPARATION_INTERRUPTED"
		if claim.attempts >= ProcessAttempts {
			assetStatus, code = "validation_failed", "PREPARATION_ATTEMPTS_EXHAUSTED"
		}
		if err := saveProcessFailure(op, tx, claim.id, assetStatus, code); err != nil {
			return 0, err
		}
		if assetStatus == "uploaded_unvalidated" {
			dedupe := fmt.Sprintf("process:%s:recovery:%d", claim.id, claim.attempts)
			if err := processEvent(op, tx, claim.id, dedupe, 0); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, processDBError(ctx, op)
	}
	return len(claims), nil
}
