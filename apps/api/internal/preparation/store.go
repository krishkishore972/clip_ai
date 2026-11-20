package preparation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type SQLStore struct{ DB *sql.DB }

func validateStatus(in Status) (Status, error) {
	if in.AssetID == "" || in.Attempts < 0 || in.Attempts > MaxAttempts || in.UpdatedAt.IsZero() {
		return Status{}, ErrDatabase
	}
	switch in.Status {
	case "not_prepared":
		if in.Attempts != 0 || in.FailureCode != nil {
			return Status{}, ErrDatabase
		}
	case "pending", "preparing", "ready", "failed":
	default:
		return Status{}, ErrDatabase
	}
	if in.Status == "preparing" && in.Attempts == 0 {
		return Status{}, ErrDatabase
	}
	if in.Status == "ready" {
		if in.Attempts == 0 || in.PreparedAt == nil || in.PreparedAt.IsZero() || in.FailureCode != nil {
			return Status{}, ErrDatabase
		}
		preparedAt := in.PreparedAt.UTC()
		in.PreparedAt = &preparedAt
	} else if in.PreparedAt != nil {
		return Status{}, ErrDatabase
	}
	if in.Status == "failed" && in.FailureCode == nil {
		return Status{}, ErrDatabase
	}
	if in.Status == "preparing" && in.FailureCode != nil {
		return Status{}, ErrDatabase
	}
	if in.FailureCode != nil {
		code := SafeFailureCode(*in.FailureCode)
		in.FailureCode = &code
	}
	in.UpdatedAt = in.UpdatedAt.UTC()
	return in, nil
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
	err := s.DB.QueryRowContext(ctx, query, id, owner).Scan(&out.AssetID, &out.Status, &out.Attempts, &code, &out.UpdatedAt, &prepared)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, dbError(ctx)
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	if code.Valid {
		v := SafeFailureCode(code.String)
		out.FailureCode = &v
	}
	if prepared.Valid {
		v := prepared.Time.UTC()
		out.PreparedAt = &v
	}
	return validateStatus(out)
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

func (s SQLStore) Claim(ctx context.Context, id, token string, lease time.Duration) (Claim, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(token) == "" || lease < time.Microsecond {
		return Claim{}, ErrArguments
	}
	if s.DB == nil {
		return Claim{}, ErrDatabase
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return Claim{}, dbError(op)
	}
	defer tx.Rollback()
	var out Claim
	var source string
	var raw []byte
	err = tx.QueryRowContext(op, `
		SELECT
		    id,
		    storage_bucket,
		    storage_key,
		    size_bytes,
		    status,
		    probed_metadata
		FROM media_assets
		WHERE id = $1
		    AND user_id IS NOT NULL
		FOR UPDATE
	`, id).Scan(&out.AssetID, &out.Bucket, &out.Key, &out.SizeBytes, &source, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, nil
	}
	if err != nil {
		return Claim{}, dbError(op)
	}
	if source != "ready" {
		return Claim{}, nil
	}
	var status string
	var live bool
	err = tx.QueryRowContext(op, `
		SELECT
		    status,
		    attempts,
		    COALESCE(lease_until > clock_timestamp(), false)
		FROM media_preparations
		WHERE asset_id = $1
		FOR UPDATE
	`, id).Scan(&status, &out.Attempts, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, nil
	}
	if err != nil {
		return Claim{}, dbError(op)
	}
	if status == "ready" || status == "failed" {
		return Claim{}, nil
	}
	if status == "preparing" && live {
		return Claim{}, ErrBusy
	}
	if out.Attempts >= MaxAttempts {
		if err := failOrRetry(op, tx, id, out.Attempts, "PREPARATION_ATTEMPTS_EXHAUSTED", false); err != nil {
			return Claim{}, err
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, dbError(op)
		}
		return Claim{}, nil
	}
	if err := json.Unmarshal(raw, &out.Metadata); err != nil || !validMetadata(out) {
		return Claim{}, ErrArguments
	}
	out.OutputID, err = randomID()
	if err != nil {
		return Claim{}, err
	}
	// Never reuse a fencing token as an externally observable output namespace.
	for out.OutputID == token {
		out.OutputID, err = randomID()
		if err != nil {
			return Claim{}, err
		}
	}
	_, err = tx.ExecContext(op, `
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
	`, id, token, out.OutputID, lease.Microseconds())
	if err != nil {
		return Claim{}, dbError(op)
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, dbError(op)
	}
	out.Token = token
	out.Attempts++
	return out, nil
}

func validMetadata(c Claim) bool {
	m := c.Metadata
	if m.SizeBytes != c.SizeBytes || m.SizeBytes <= 0 || m.SizeBytes > 2_000_000_000 || m.DurationMs <= 0 || m.DurationMs > 7_200_000 || len(m.Video) == 0 || len(m.Audio) == 0 {
		return false
	}
	for _, v := range m.Video {
		if v.Index < 0 || v.Width <= 0 || v.Height <= 0 || v.FPS.Num <= 0 || v.FPS.Den <= 0 {
			return false
		}
	}
	for _, a := range m.Audio {
		if a.Index < 0 || a.SampleRate <= 0 || a.Channels <= 0 {
			return false
		}
	}
	return true
}
