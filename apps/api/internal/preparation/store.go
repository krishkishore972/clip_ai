package preparation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// SQLStore serves owned status reads and idempotent preparation requests.
// It does not claim or complete worker attempts.
type SQLStore struct{ DB *sql.DB }

// CheckSchema verifies the preparation table without reading application rows
// or applying migrations.
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
		    audio_start_ms,
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
	if err := rows.Err(); err != nil {
		return dbError(ctx)
	}
	return nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrDatabase
	}
	return hex.EncodeToString(b[:]), nil
}

func dbError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrDatabase
}

// InsertPending runs within the caller's asset-locked transaction. It inserts
// an event only for a newly created row, never resetting an existing attempt.
func InsertPending(ctx context.Context, tx *sql.Tx, assetID string) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	const query = `
		WITH inserted AS (
		    INSERT INTO media_preparations (asset_id)
		    VALUES ($1)
		    ON CONFLICT (asset_id) DO NOTHING
		    RETURNING asset_id
		)
		INSERT INTO outbox_events (
		    id,
		    asset_id,
		    task_type,
		    queue_name,
		    payload,
		    dedupe_key
		)
		SELECT
		    $2,
		    asset_id,
		    'media:process',
		    'processing',
		    jsonb_build_object('assetId', asset_id),
		    'prepare:' || asset_id || ':v1'
		FROM inserted
		ON CONFLICT (dedupe_key) DO NOTHING
	`
	if _, err := tx.ExecContext(ctx, query, assetID, id); err != nil {
		return dbError(ctx)
	}
	return nil
}

func (s SQLStore) GetStatus(ctx context.Context, owner, id string) (Status, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(id) == "" {
		return Status{}, ErrNotFound
	}
	if s.DB == nil {
		return Status{}, ErrDatabase
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const query = `
		SELECT
		    a.id,
		    COALESCE(p.status, 'not_prepared'),
		    COALESCE(p.attempts, 0),
		    p.failure_code,
		    COALESCE(p.updated_at, a.updated_at),
		    p.prepared_at
		FROM media_assets a
		LEFT JOIN media_preparations p ON p.asset_id = a.id
		WHERE a.id = $1
		    AND a.user_id = $2
	`
	var out Status
	var code sql.NullString
	var prepared sql.NullTime
	err := s.DB.QueryRowContext(ctx, query, id, owner).Scan(
		&out.AssetID,
		&out.Status,
		&out.Attempts,
		&code,
		&out.UpdatedAt,
		&prepared,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, dbError(ctx)
	}
	if code.Valid {
		out.FailureCode = &code.String
	}
	if prepared.Valid {
		out.PreparedAt = &prepared.Time
	}
	return SanitizeStatus(out)
}

func (s SQLStore) Ensure(ctx context.Context, owner, id string) (Status, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(id) == "" {
		return Status{}, ErrNotFound
	}
	if s.DB == nil {
		return Status{}, ErrDatabase
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return Status{}, dbError(op)
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(op, `
		SELECT status
		FROM media_assets
		WHERE id = $1
		    AND user_id = $2
		FOR UPDATE
	`, id, owner).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, dbError(op)
	}
	if status != "ready" {
		return Status{}, ErrNotReady
	}
	if err := InsertPending(op, tx, id); err != nil {
		return Status{}, err
	}
	if err := tx.Commit(); err != nil {
		return Status{}, dbError(op)
	}
	return s.GetStatus(ctx, owner, id)
}
