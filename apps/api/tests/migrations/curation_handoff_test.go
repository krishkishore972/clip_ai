package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func finishCurationTranscript(t *testing.T, ctx context.Context, pool *sql.DB, id string, words int) {
	t.Helper()
	enqueueTranscriptFile(t, ctx, pool, id)
	store := transcription.SQLStore{DB: pool}
	claim, err := store.Claim(ctx, id, id+"-claim", time.Minute)
	if err != nil || claim.AssetID != id {
		t.Fatalf("claim transcript fixture: %v", err)
	}
	err = store.Finish(ctx, claim, &transcription.Publication{
		Object: storage.DerivedObject{
			Key:       transcription.TranscriptKey(id, claim.OutputID),
			SHA256:    strings.Repeat("c", 64),
			SizeBytes: 123,
		},
		WordCount:  words,
		DurationMs: 1000,
	}, "", false)
	if err != nil {
		t.Fatal(err)
	}
}

func commitCurationHandoff(ctx context.Context, pool *sql.DB, id string) (bool, error) {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	inserted, err := curation.EnqueueReadyTranscript(ctx, tx, id)
	if err != nil {
		return false, err
	}
	return inserted, tx.Commit()
}

func countCurationEvents(t *testing.T, ctx context.Context, pool *sql.DB, id string) int {
	t.Helper()
	var count int
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM outbox_events
		WHERE asset_id = $1 AND task_type = $2
	`, id, curation.TaskType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCurationHandoffRequiresTransaction(t *testing.T) {
	if _, err := curation.EnqueueReadyTranscript(context.Background(), nil, "asset-1"); !errors.Is(err, curation.ErrConfiguration) {
		t.Fatal("nil transaction must be rejected")
	}
}

func TestCurationTranscriptHandoff(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	t.Run("snapshot and identifier-only deduplicated event", func(t *testing.T) {
		const id = "handoff-ready"
		finishCurationTranscript(t, ctx, pool, id, 100)
		inserted, err := commitCurationHandoff(ctx, pool, id)
		if err != nil || !inserted {
			t.Fatalf("handoff not inserted: %v", err)
		}
		var same bool
		err = pool.QueryRowContext(ctx, `
			SELECT
			    j.transcript_bucket = t.storage_bucket
			    AND j.transcript_key = t.transcript_key
			    AND j.transcript_sha256 = t.transcript_sha256
			    AND j.transcript_size_bytes = t.transcript_size_bytes
			    AND j.transcript_word_count = t.word_count
			    AND j.source_duration_ms = t.source_duration_ms
			    AND j.candidate_count = 1
			    AND j.min_duration_ms = 1
			    AND j.max_duration_ms = 1000
			    AND j.status = 'pending'
			    AND j.attempts = 0
			    AND e.queue_name = $2
			    AND e.payload = jsonb_build_object('assetId', j.asset_id)
			FROM media_curation_jobs j
			JOIN media_transcript_files t ON t.asset_id = j.asset_id
			JOIN outbox_events e ON e.asset_id = j.asset_id AND e.task_type = $3
			WHERE j.asset_id = $1
		`, id, curation.Queue, curation.TaskType).Scan(&same)
		if err != nil || !same {
			t.Fatal("snapshot, preferences or minimal payload drift")
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE outbox_events
			SET dispatched_at = CURRENT_TIMESTAMP
			WHERE asset_id = $1 AND task_type = $2
		`, id, curation.TaskType); err != nil {
			t.Fatal(err)
		}
		if inserted, err := commitCurationHandoff(ctx, pool, id); err != nil || inserted {
			t.Fatalf("replay created new work: %v", err)
		}
		if countCurationEvents(t, ctx, pool, id) != 1 {
			t.Fatal("duplicate logical handoff")
		}
		var delivered bool
		if err := pool.QueryRowContext(ctx, `
			SELECT dispatched_at IS NOT NULL
			FROM outbox_events
			WHERE asset_id = $1 AND task_type = $2
		`, id, curation.TaskType).Scan(&delivered); err != nil || !delivered {
			t.Fatal("replay reset dispatcher bookkeeping")
		}
	})
	t.Run("concurrent publication inserts one event", func(t *testing.T) {
		const id = "handoff-concurrent"
		finishCurationTranscript(t, ctx, pool, id, 100)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := commitCurationHandoff(ctx, pool, id)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if countCurationEvents(t, ctx, pool, id) != 1 {
			t.Fatal("concurrent publishers created duplicate events")
		}
	})
	t.Run("missing preferences is an error", func(t *testing.T) {
		const id = "handoff-missing-preferences"
		finishCurationTranscript(t, ctx, pool, id, 100)
		if _, err := pool.ExecContext(ctx, `
			DELETE FROM media_curation_jobs WHERE asset_id = $1
		`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := commitCurationHandoff(ctx, pool, id); !errors.Is(err, curation.ErrNoPreferences) {
			t.Fatalf("asset without preferences was not rejected: %v", err)
		}
		if countCurationEvents(t, ctx, pool, id) != 0 {
			t.Fatal("asset without preferences queued curation")
		}
	})
	t.Run("ineligible transcript or NULL ownership fails handoff", func(t *testing.T) {
		const id = "handoff-not-ready"
		enqueueTranscriptFile(t, ctx, pool, id)
		if _, err := commitCurationHandoff(ctx, pool, id); !errors.Is(err, curation.ErrNoTranscript) {
			t.Fatal("pending transcript was accepted for handoff")
		}
		const unowned = "handoff-unowned"
		finishCurationTranscript(t, ctx, pool, unowned, 100)
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_assets SET user_id = NULL WHERE id = $1
		`, unowned); err != nil {
			t.Fatal(err)
		}
		if _, err := commitCurationHandoff(ctx, pool, unowned); !errors.Is(err, curation.ErrNoTranscript) {
			t.Fatal("NULL-owned transcript was accepted for handoff")
		}
		if countCurationEvents(t, ctx, pool, unowned) != 0 {
			t.Fatal("NULL-owned transcript queued curation")
		}
	})
	t.Run("empty and maximum-sized transcripts can be handed off", func(t *testing.T) {
		for id, words := range map[string]int{
			"handoff-empty": 0,
			"handoff-max":   curation.MaxTranscriptWords,
		} {
			finishCurationTranscript(t, ctx, pool, id, words)
			if inserted, err := commitCurationHandoff(ctx, pool, id); err != nil || !inserted {
				t.Fatalf("valid transcript not handed off: %v", err)
			}
		}
		// A future consumer will short-circuit empty transcripts without Groq.
	})
	t.Run("oversized input fails only curation without a queue event", func(t *testing.T) {
		const id = "handoff-too-large"
		finishCurationTranscript(t, ctx, pool, id, curation.MaxTranscriptWords+1)
		if inserted, err := commitCurationHandoff(ctx, pool, id); err != nil || inserted {
			t.Fatalf("oversized input was queued or broke transcript publication: %v", err)
		}
		var safe bool
		if err := pool.QueryRowContext(ctx, `
			SELECT
			    j.status = 'failed'
			    AND j.failure_code = 'CURATION_INPUT_TOO_LARGE'
			    AND j.completed_at IS NOT NULL
			    AND j.attempts = 0
			    AND j.transcript_sha256 IS NULL
			    AND t.status = 'ready'
			FROM media_curation_jobs j
			JOIN media_transcript_files t ON t.asset_id = j.asset_id
			WHERE j.asset_id = $1
		`, id).Scan(&safe); err != nil || !safe || countCurationEvents(t, ctx, pool, id) != 0 {
			t.Fatal("oversized curation harmed the ready transcript or queued work")
		}
	})
	t.Run("outbox failure rolls back the snapshot", func(t *testing.T) {
		const id = "handoff-rollback"
		finishCurationTranscript(t, ctx, pool, id, 100)
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE outbox_events
			ADD CONSTRAINT reject_curation_handoff_fixture CHECK (
			    task_type <> 'media:curate' OR asset_id <> 'handoff-rollback'
			)
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := commitCurationHandoff(ctx, pool, id); !errors.Is(err, curation.ErrDatabase) {
			t.Fatalf("expected safe database error, got %v", err)
		}
		var unbound bool
		if err := pool.QueryRowContext(ctx, `
			SELECT transcript_sha256 IS NULL AND status = 'pending' AND attempts = 0
			FROM media_curation_jobs WHERE asset_id = $1
		`, id).Scan(&unbound); err != nil || !unbound || countCurationEvents(t, ctx, pool, id) != 0 {
			t.Fatal("failed handoff left a partial snapshot/event")
		}
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE outbox_events DROP CONSTRAINT reject_curation_handoff_fixture
		`); err != nil {
			t.Fatal(err)
		}
		if inserted, err := commitCurationHandoff(ctx, pool, id); err != nil || !inserted {
			t.Fatal("handoff did not recover after the database fault")
		}
	})
	t.Run("caller rollback discards both writes", func(t *testing.T) {
		const id = "handoff-caller-rollback"
		finishCurationTranscript(t, ctx, pool, id, 100)
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if inserted, err := curation.EnqueueReadyTranscript(ctx, tx, id); err != nil || !inserted {
			t.Fatal("transactional handoff failed")
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var unbound bool
		if err := pool.QueryRowContext(ctx, `
			SELECT transcript_sha256 IS NULL FROM media_curation_jobs WHERE asset_id = $1
		`, id).Scan(&unbound); err != nil || !unbound || countCurationEvents(t, ctx, pool, id) != 0 {
			t.Fatal("helper committed the caller's transaction")
		}
	})
	t.Run("existing snapshot cannot be replaced", func(t *testing.T) {
		const id = "handoff-conflict"
		finishCurationTranscript(t, ctx, pool, id, 100)
		if _, err := commitCurationHandoff(ctx, pool, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_transcript_files SET transcript_sha256 = $2 WHERE asset_id = $1
		`, id, strings.Repeat("d", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := commitCurationHandoff(ctx, pool, id); !errors.Is(err, curation.ErrConflict) {
			t.Fatal("changed transcript silently replaced the bound input")
		}
		var hash string
		if err := pool.QueryRowContext(ctx, `
			SELECT transcript_sha256 FROM media_curation_jobs WHERE asset_id = $1
		`, id).Scan(&hash); err != nil || hash != strings.Repeat("c", 64) {
			t.Fatal("conflict modified the existing snapshot")
		}
	})
	t.Run("wrong event under the same dedupe key is not accepted", func(t *testing.T) {
		const id = "handoff-event-conflict"
		finishCurationTranscript(t, ctx, pool, id, 100)
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO outbox_events (
			    id, asset_id, task_type, queue_name, payload, dedupe_key
			)
			VALUES ('collision-event', $1, 'wrong-task', 'wrong-queue', '{}', $2)
		`, id, "curate:"+id+":"+strings.Repeat("c", 64)+":initial"); err != nil {
			t.Fatal(err)
		}
		if _, err := commitCurationHandoff(ctx, pool, id); !errors.Is(err, curation.ErrConflict) {
			t.Fatal("conflicting event was treated as successful delivery")
		}
		var unbound bool
		if err := pool.QueryRowContext(ctx, `
			SELECT transcript_sha256 IS NULL FROM media_curation_jobs WHERE asset_id = $1
		`, id).Scan(&unbound); err != nil || !unbound {
			t.Fatal("event collision left a partial snapshot")
		}
	})
	t.Run("started and terminal jobs are not requeued", func(t *testing.T) {
		const id = "handoff-started"
		finishCurationTranscript(t, ctx, pool, id, 100)
		if _, err := commitCurationHandoff(ctx, pool, id); err != nil {
			t.Fatal(err)
		}
		// Simulate a missing old event to prove the helper does not recreate
		// paid work once an execution has started, even if status is pending.
		if _, err := pool.ExecContext(ctx, `
			DELETE FROM outbox_events WHERE asset_id = $1 AND task_type = $2
		`, id, curation.TaskType); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{"pending", "running", "ready", "failed"} {
			_, err := pool.ExecContext(ctx, `
				UPDATE media_curation_jobs
				SET
				    status = $2,
				    attempts = 1,
				    started_at = CURRENT_TIMESTAMP,
				    claim_token = CASE WHEN $2 = 'running' THEN 'fixture-claim' END,
				    lease_until = CASE WHEN $2 = 'running' THEN CURRENT_TIMESTAMP + INTERVAL '1 minute' END,
				    completed_at = CASE WHEN $2 IN ('ready', 'failed') THEN CURRENT_TIMESTAMP END,
				    failure_code = CASE WHEN $2 = 'failed' THEN 'CURATION_FAILED' END
				WHERE asset_id = $1
			`, id, status)
			if err != nil {
				t.Fatal(err)
			}
			if inserted, err := commitCurationHandoff(ctx, pool, id); err != nil || inserted {
				t.Fatalf("started %s job received fresh initial work: %v", status, err)
			}
			if countCurationEvents(t, ctx, pool, id) != 0 {
				t.Fatal("helper invented a worker retry policy")
			}
		}
	})
}
