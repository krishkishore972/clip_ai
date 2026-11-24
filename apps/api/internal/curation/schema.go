package curation

import (
	"context"
	"database/sql"
)

// CheckSchema is read-only and runs before a curation consumer is started.
// It verifies columns used by claims, publication and recovery without reading
// user rows, applying migrations or probing a provider.
func CheckSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrConfiguration
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
		    j.asset_id,
		    j.candidate_count,
		    j.min_duration_ms,
		    j.max_duration_ms,
		    j.status,
		    j.attempts,
		    j.claim_token,
		    j.lease_until,
		    j.failure_code,
		    j.recovery_generation,
		    j.next_attempt_at,
		    j.transcript_bucket,
		    j.transcript_key,
		    j.transcript_sha256,
		    j.transcript_size_bytes,
		    j.transcript_word_count,
		    j.source_duration_ms,
		    j.model,
		    j.prompt_version,
		    j.started_at,
		    j.completed_at,
		    j.updated_at,
		    c.id,
		    c.asset_id,
		    c.candidate_index,
		    c.start_word_index,
		    c.end_word_index_exclusive,
		    c.start_ms,
		    c.end_ms,
		    c.title,
		    c.hook,
		    c.reason,
		    e.id,
		    e.dedupe_key,
		    e.task_type,
		    e.queue_name,
		    e.payload,
		    e.next_attempt_at
		FROM media_curation_jobs j
		LEFT JOIN media_curation_candidates c ON c.asset_id = j.asset_id
		LEFT JOIN outbox_events e ON e.asset_id = j.asset_id
		LIMIT 0
	`)
	if err != nil {
		return storeError(ctx)
	}
	if err := rows.Close(); err != nil {
		return storeError(ctx)
	}
	return nil
}
