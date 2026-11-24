package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func curationSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) || postgres.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %v", code, err)
	}
}

func insertCurationAsset(t *testing.T, ctx context.Context, pool *sql.DB, id string) {
	t.Helper()
	_, err := pool.ExecContext(ctx, `
		INSERT INTO media_assets (
		    id,
		    storage_bucket,
		    storage_key,
		    declared_content_type,
		    size_bytes
		)
		VALUES ($1, 'fixture-bucket', $1 || '/original.mp4', 'video/mp4', 1024)
	`, id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCurationMigration(t *testing.T) {
	// This helper verifies a uniquely named schema before running any migration.
	// Supply only a disposable, direct/session TEST_DATABASE_URL.
	ctx, pool, provider := transcriptFileDatabase(t)
	// Start from the pre-curation schema so the Up below is observable.
	if result, err := provider.Down(ctx); err != nil || result.Source.Version != 9 {
		t.Fatalf("pre-curation rollback failed: %v", err)
	}
	insertCurationAsset(t, ctx, pool, "existing-before-curation")

	up, err := provider.UpTo(ctx, 9)
	if err != nil || len(up) != 1 || up[0].Source.Version != 9 {
		t.Fatalf("curation Up failed: %v", err)
	}
	var count int
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM media_curation_jobs
	`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration backfilled an existing asset")
	}
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM outbox_events
	`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration created queue work")
	}
	if result, err := provider.Down(ctx); err != nil || result.Source.Version != 9 {
		t.Fatalf("empty curation Down failed: %v", err)
	}
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if again, err := provider.UpTo(ctx, 9); err != nil || len(again) != 0 {
		t.Fatal("repeat curation Up is not a no-op")
	}

	insertCurationAsset(t, ctx, pool, "curation-fixture")
	_, err = pool.ExecContext(ctx, `
		INSERT INTO media_curation_jobs (
		    asset_id,
		    candidate_count,
		    min_duration_ms,
		    max_duration_ms
		)
		VALUES ('curation-fixture', 3, 15000, 60000)
	`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("rollback must refuse even a pending job without candidates")
	}
	t.Run("preferences and lifecycle constraints", func(t *testing.T) {
		for name, assignment := range map[string]string{
			"count zero":       "candidate_count = 0",
			"count over five":  "candidate_count = 6",
			"duration zero":    "min_duration_ms = 0",
			"duration order":   "min_duration_ms = 60000, max_duration_ms = 15000",
			"duration ceiling": "max_duration_ms = 60001",
			"unknown state":    "status = 'accepted'",
			"negative claims":  "attempts = -1",
			"partial snapshot": "transcript_key = 'private/transcript.json'",
			"orphan lease":     "lease_until = CURRENT_TIMESTAMP",
			"unclaimed run":    "status = 'running'",
			"empty token":      "claim_token = ''",
			"premature ready":  "status = 'ready', completed_at = CURRENT_TIMESTAMP",
			"failure code":     "failure_code = 'private details'",
			"failed no code":   "status = 'failed', completed_at = CURRENT_TIMESTAMP",
		} {
			t.Run(name, func(t *testing.T) {
				// Assignments are static test literals, never external input.
				_, err := pool.ExecContext(ctx,
					"UPDATE media_curation_jobs SET "+assignment+" WHERE asset_id = 'curation-fixture'",
				)
				curationSQLState(t, err, "23514")
			})
		}
	})
	t.Run("foreign key and one initial job", func(t *testing.T) {
		_, err := pool.ExecContext(ctx, `
			INSERT INTO media_curation_jobs (
			    asset_id,
			    candidate_count,
			    min_duration_ms,
			    max_duration_ms
			)
			VALUES ('missing-asset', 1, 15000, 60000)
		`)
		curationSQLState(t, err, "23503")
		_, err = pool.ExecContext(ctx, `
			INSERT INTO media_curation_jobs (
			    asset_id,
			    candidate_count,
			    min_duration_ms,
			    max_duration_ms
			)
			VALUES ('curation-fixture', 1, 15000, 60000)
		`)
		curationSQLState(t, err, "23505")
	})

	_, err = pool.ExecContext(ctx, `
		UPDATE media_curation_jobs
		SET
		    transcript_bucket = 'fixture-bucket',
		    transcript_key = 'private/transcript.json',
		    transcript_sha256 = $1,
		    transcript_size_bytes = 1024,
		    transcript_word_count = 100,
		    source_duration_ms = 400000
		WHERE asset_id = 'curation-fixture'
	`, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range []string{
		"transcript_sha256 = 'invalid'",
		"transcript_size_bytes = 0",
		"transcript_word_count = 12001",
		"source_duration_ms = 7200001",
	} {
		_, err := pool.ExecContext(ctx,
			"UPDATE media_curation_jobs SET "+assignment+" WHERE asset_id = 'curation-fixture'",
		)
		curationSQLState(t, err, "23514")
	}
	_, err = pool.ExecContext(ctx, `
		UPDATE media_curation_jobs
		SET
		    status = 'running',
		    attempts = 1,
		    claim_token = 'fixture-claim',
		    lease_until = CURRENT_TIMESTAMP + INTERVAL '2 minutes',
		    started_at = CURRENT_TIMESTAMP
		WHERE asset_id = 'curation-fixture'
	`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.ExecContext(ctx, `
		INSERT INTO media_curation_candidates (
		    id,
		    asset_id,
		    candidate_index,
		    start_word_index,
		    end_word_index_exclusive,
		    start_ms,
		    end_ms,
		    title,
		    hook,
		    reason
		)
		VALUES
		    ('candidate-one', 'curation-fixture', 0, 0, 50, 0, 15000, 'Title', 'Hook', 'Reason'),
		    ('candidate-two', 'curation-fixture', 1, 10, 60, 4000, 20000, 'Title', 'Hook', 'Reason')
	`)
	if err != nil {
		t.Fatalf("overlapping candidates must be allowed: %v", err)
	}
	t.Run("candidate row bounds", func(t *testing.T) {
		for name, assignment := range map[string]string{
			"index ceiling":    "candidate_index = 5",
			"negative index":   "start_word_index = -1",
			"empty range":      "end_word_index_exclusive = start_word_index",
			"word ceiling":     "end_word_index_exclusive = 12001",
			"negative time":    "start_ms = -1",
			"zero duration":    "end_ms = start_ms",
			"duration ceiling": "end_ms = 60001",
			"blank title":      "title = ''",
			"metadata ceiling": "reason = repeat('x', 1001)",
		} {
			t.Run(name, func(t *testing.T) {
				_, err := pool.ExecContext(ctx,
					"UPDATE media_curation_candidates SET "+assignment+" WHERE id = 'candidate-one'",
				)
				curationSQLState(t, err, "23514")
			})
		}
		_, err := pool.ExecContext(ctx, `
			UPDATE media_curation_candidates
			SET candidate_index = 0
			WHERE id = 'candidate-two'
		`)
		curationSQLState(t, err, "23505")
		_, err = pool.ExecContext(ctx, `
			UPDATE media_curation_candidates
			SET
			    start_word_index = 0,
			    end_word_index_exclusive = 50
			WHERE id = 'candidate-two'
		`)
		curationSQLState(t, err, "23505")
	})
	_, err = pool.ExecContext(ctx, `
		UPDATE media_curation_jobs
		SET
		    status = 'ready',
		    claim_token = NULL,
		    lease_until = NULL,
		    completed_at = CURRENT_TIMESTAMP
		WHERE asset_id = 'curation-fixture'
	`)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty results and terminal failures are representable", func(t *testing.T) {
		insertCurationAsset(t, ctx, pool, "curation-empty")
		_, err := pool.ExecContext(ctx, `
			INSERT INTO media_curation_jobs (
			    asset_id,
			    candidate_count,
			    min_duration_ms,
			    max_duration_ms,
			    status,
			    attempts,
			    transcript_bucket,
			    transcript_key,
			    transcript_sha256,
			    transcript_size_bytes,
			    transcript_word_count,
			    source_duration_ms,
			    started_at,
			    completed_at
			)
			SELECT
			    'curation-empty',
			    5,
			    1,
			    60000,
			    'ready',
			    1,
			    transcript_bucket,
			    transcript_key,
			    transcript_sha256,
			    transcript_size_bytes,
			    0,
			    source_duration_ms,
			    CURRENT_TIMESTAMP,
			    CURRENT_TIMESTAMP
			FROM media_curation_jobs
			WHERE asset_id = 'curation-fixture'
		`)
		if err != nil {
			t.Fatalf("empty ready result must be permitted: %v", err)
		}
		insertCurationAsset(t, ctx, pool, "curation-failed")
		_, err = pool.ExecContext(ctx, `
			INSERT INTO media_curation_jobs (
			    asset_id,
			    candidate_count,
			    min_duration_ms,
			    max_duration_ms,
			    status,
			    failure_code,
			    completed_at
			)
			VALUES (
			    'curation-failed',
			    1,
			    15000,
			    60000,
			    'failed',
			    'CURATION_SOURCE_UNAVAILABLE',
			    CURRENT_TIMESTAMP
			)
		`)
		if err != nil {
			t.Fatalf("safe terminal source failure must be permitted: %v", err)
		}
	})
	_, err = pool.ExecContext(ctx, `
		DELETE FROM media_curation_jobs WHERE asset_id = 'curation-fixture'
	`)
	curationSQLState(t, err, "23503")
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("nonempty curation rollback must refuse")
	}
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM media_curation_candidates
	`).Scan(&count); err != nil || count != 2 {
		t.Fatal("refused rollback damaged candidate rows")
	}
	var status, key string
	if err := pool.QueryRowContext(ctx, `
		SELECT status, storage_key FROM media_assets
		WHERE id = 'existing-before-curation'
	`).Scan(&status, &key); err != nil ||
		status != "uploaded_unvalidated" || key != "existing-before-curation/original.mp4" {
		t.Fatal("curation migration modified the original asset")
	}
}
