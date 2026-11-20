package preparation

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s SQLStore) ArchiveCandidates(ctx context.Context, after string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, ErrArguments
	}
	if s.DB == nil {
		return nil, ErrDatabase
	}
	const query = `
		SELECT
		    a.id,
		    e.id
		FROM media_assets a
		JOIN media_preparations p ON p.asset_id = a.id
		JOIN LATERAL (
		    SELECT
		        id,
		        dispatched_at
		    FROM outbox_events
		    WHERE asset_id = a.id
		        AND task_type = 'media:prepare'
		        AND queue_name = 'preparation'
		    ORDER BY created_at DESC, id DESC
		    LIMIT 1
		) e ON true
		WHERE a.status = 'ready'
		    AND a.user_id IS NOT NULL
		    AND p.status = 'pending'
		    AND a.id > $1
		    AND e.dispatched_at IS NOT NULL
		ORDER BY a.id
		LIMIT $2
	`
	rows, err := s.DB.QueryContext(ctx, query, after, min(limit, 100))
	if err != nil {
		return nil, dbError(ctx)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.AssetID, &d.EventID); err != nil {
			return nil, dbError(ctx)
		}
		out = append(out, d)
	}
	if rows.Err() != nil {
		return nil, dbError(ctx)
	}
	return out, nil
}

// FailArchived requires positive archived evidence from the caller, never a
// missing task. Rechecking the latest dispatched event protects newer retries.
func (s SQLStore) FailArchived(ctx context.Context, d Delivery) error {
	if d.AssetID == "" || d.EventID == "" {
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
	var id string
	err = tx.QueryRowContext(op, `
		SELECT id
		FROM media_assets
		WHERE id = $1
		    AND user_id IS NOT NULL
		    AND status = 'ready'
		FOR UPDATE
	`, d.AssetID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return dbError(op)
	}
	// Lock the prep row before evaluating event freshness to avoid a stale
	// statement snapshot authorizing a write after a concurrent retry finish.
	err = tx.QueryRowContext(op, `
		SELECT asset_id
		FROM media_preparations
		WHERE asset_id = $1
		FOR UPDATE
	`, id).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return dbError(op)
	}
	_, err = tx.ExecContext(op, `
		UPDATE media_preparations p
		SET
		    status = 'failed',
		    failure_code = 'PREPARATION_RETRIES_EXHAUSTED',
		    updated_at = clock_timestamp()
		WHERE p.asset_id = $1
		    AND p.status = 'pending'
		    AND p.claim_token IS NULL
		    AND $2 = (
		        SELECT id
		        FROM outbox_events
		        WHERE asset_id = p.asset_id
		            AND task_type = 'media:prepare'
		            AND queue_name = 'preparation'
		        ORDER BY created_at DESC, id DESC
		        LIMIT 1
		    )
		    AND EXISTS (
		        SELECT 1
		        FROM outbox_events
		        WHERE id = $2
		            AND dispatched_at IS NOT NULL
		    )
	`, id, d.EventID)
	if err != nil {
		return dbError(op)
	}
	if err := tx.Commit(); err != nil {
		return dbError(op)
	}
	return nil
}

// CheckSchema is read-only and does not apply migrations or read asset rows.
func CheckSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDatabase
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
		    asset_id,
		    pipeline_version,
		    status,
		    attempts,
		    failure_code,
		    claim_token,
		    lease_until,
		    output_id,
		    storage_bucket,
		    audio_key,
		    audio_sha256,
		    source_sha256,
		    audio_size_bytes,
		    created_at,
		    updated_at,
		    prepared_at
		FROM media_preparations
		LIMIT 0
	`)
	if err != nil {
		return dbError(ctx)
	}
	defer rows.Close()
	if rows.Err() != nil {
		return dbError(ctx)
	}
	return nil
}
