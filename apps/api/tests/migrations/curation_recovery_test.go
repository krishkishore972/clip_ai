package migrations_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/curation"
)

func TestCurationRecovery(t *testing.T) {
	ctx, pool := curationStoreDatabase(t)
	store := curation.SQLStore{DB: pool}
	t.Run("concurrent expired claim recovery and hard attempt cap", func(t *testing.T) {
		c, data := claimCurationFixture(t, ctx, pool, "recover-curation-expired")
		pub := curationPublication(t, c, data)
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE asset_id = $1
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		counts := make(chan int, 4)
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n, err := store.Recover(ctx, 100)
				counts <- n
				errs <- err
			}()
		}
		wg.Wait()
		close(counts)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		total := 0
		for n := range counts {
			total += n
		}
		if total != 1 || countCurationEvents(t, ctx, pool, c.AssetID) != 2 {
			t.Fatal("concurrent recovery duplicated work")
		}
		assertCurationState(t, ctx, pool, c.AssetID, "pending", 1, 0)
		if err := store.Finish(ctx, c, pub, "", false); !errors.Is(err, curation.ErrStale) {
			t.Fatal("recovered claim still published")
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 0 {
			t.Fatal("fresh recovery repeated")
		}
		if c, err := store.Claim(ctx, c.AssetID, "too-early"); err != nil || c.AssetID != "" {
			t.Fatal("recovered job ignored retry backoff")
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs SET next_attempt_at = clock_timestamp() WHERE asset_id = $1
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		second, err := store.Claim(ctx, c.AssetID, "recovered-second")
		if err != nil || second.Attempts != 2 {
			t.Fatalf("second recovered claim: %v", err)
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE asset_id = $1
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatalf("final recovery: %d %v", n, err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "failed", 2, 0)
		var code string
		if err := pool.QueryRowContext(ctx, `
			SELECT failure_code FROM media_curation_jobs WHERE asset_id = $1
		`, c.AssetID).Scan(&code); err != nil || code != "CURATION_RETRIES_EXHAUSTED" {
			t.Fatal("expired second claim did not exhaust")
		}
		if countCurationEvents(t, ctx, pool, c.AssetID) != 2 {
			t.Fatal("exhaustion created extra paid work")
		}
		if next, err := store.Claim(ctx, c.AssetID, "third"); err != nil || next.AssetID != "" {
			t.Fatal("third claim accepted")
		}
	})
	t.Run("lost delivery is repaired only after five minutes", func(t *testing.T) {
		const id = "recover-curation-pending"
		enqueueCurationFixture(t, ctx, pool, id, false)
		enqueueTranscriptFile(t, ctx, pool, "recover-curation-unbound")
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs SET updated_at = clock_timestamp() - INTERVAL '4 minutes'
			WHERE asset_id = $1
		`, id); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 0 {
			t.Fatal("premature recovery")
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs SET updated_at = clock_timestamp() - INTERVAL '6 minutes'
			WHERE asset_id IN ($1, 'recover-curation-unbound')
		`, id); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatalf("lost-delivery repair: %d %v", n, err)
		}
		assertCurationState(t, ctx, pool, id, "pending", 0, 0)
		if countCurationEvents(t, ctx, pool, id) != 2 ||
			countCurationEvents(t, ctx, pool, "recover-curation-unbound") != 0 {
			t.Fatal("recovery backfilled unbound work or failed to replace lost delivery")
		}
	})
	t.Run("source changed before recovery fails without work", func(t *testing.T) {
		c, _ := claimCurationFixture(t, ctx, pool, "recover-curation-changed")
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE asset_id = $1;
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_transcript_files SET transcript_sha256 = repeat('d', 64) WHERE asset_id = $1
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatalf("changed-source recovery: %d %v", n, err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "failed", 1, 0)
		if countCurationEvents(t, ctx, pool, c.AssetID) != 1 {
			t.Fatal("changed source requeued")
		}
	})
	t.Run("outbox failure rolls back recovery generation and claim", func(t *testing.T) {
		c, _ := claimCurationFixture(t, ctx, pool, "recover-curation-rollback")
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE asset_id = $1
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE outbox_events ADD CONSTRAINT reject_recovery_fixture CHECK (
			    dedupe_key NOT LIKE 'curate:recover-curation-rollback:%:recover:%'
			)
		`); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); n != 0 || !errors.Is(err, curation.ErrDatabase) {
			t.Fatalf("recovery fault: %d %v", n, err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "running", 1, 0)
		var unchanged bool
		if err := pool.QueryRowContext(ctx, `
			SELECT recovery_generation = 0 AND claim_token = $2
			FROM media_curation_jobs WHERE asset_id = $1
		`, c.AssetID, c.Token).Scan(&unchanged); err != nil || !unchanged {
			t.Fatal("failed recovery advanced generation or cleared claim")
		}
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE outbox_events DROP CONSTRAINT reject_recovery_fixture
		`); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatal("recovery could not retry after database fault removal")
		}
	})
}
