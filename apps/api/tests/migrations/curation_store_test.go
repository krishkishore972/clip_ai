package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func curationStoreDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, pool, provider := transcriptFileDatabase(t)
	// Runtime persistence tests also exercise the removal of the old inline
	// transcript table, unlike tests that pin a specific migration transition.
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func enqueueCurationFixture(t *testing.T, ctx context.Context, pool *sql.DB, id string, empty bool) []byte {
	t.Helper()
	enqueueTranscriptFile(t, ctx, pool, id)
	if _, err := pool.ExecContext(ctx, `
		UPDATE media_curation_jobs
		SET candidate_count = 3, min_duration_ms = 100, max_duration_ms = 1000
		WHERE asset_id = $1
	`, id); err != nil {
		t.Fatal(err)
	}
	store := transcription.SQLStore{DB: pool}
	claim, err := store.Claim(ctx, id, id+"-transcript", time.Minute)
	if err != nil || claim.AssetID != id {
		t.Fatalf("transcription claim: %v", err)
	}
	doc := transcription.Document{
		SchemaVersion: 1, AssetID: id, SourceDurationMs: 1000,
		Provider: "deepgram", Model: "nova-3", RequestedLanguage: "multi",
		Words: []transcription.Word{
			{Text: "one", StartMs: 0, EndMs: 700},
			{Text: "two", StartMs: 100, EndMs: 400},
			{Text: "three", StartMs: 200, EndMs: 900},
		},
	}
	if empty {
		doc.Words = []transcription.Word{}
	}
	data, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if err := store.Finish(ctx, claim, &transcription.Publication{
		Object: storage.DerivedObject{
			Key:    transcription.TranscriptKey(id, claim.OutputID),
			SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(data)),
		},
		WordCount: len(doc.Words), DurationMs: 1000,
	}, "", false); err != nil {
		t.Fatal(err)
	}
	return data
}

func claimCurationFixture(t *testing.T, ctx context.Context, pool *sql.DB, id string) (curation.Claim, []byte) {
	t.Helper()
	data := enqueueCurationFixture(t, ctx, pool, id, false)
	c, err := (curation.SQLStore{DB: pool}).Claim(ctx, id, id+"-claim")
	if err != nil || c.AssetID != id || c.Attempts != 1 {
		t.Fatalf("curation claim: %+v %v", c, err)
	}
	return c, data
}

func curationPublication(t *testing.T, c curation.Claim, data []byte) *curation.Publication {
	t.Helper()
	pub, err := curation.ValidateResult(c, data, []curation.Proposal{
		{StartWordIndex: 0, EndWordIndexExclusive: 2, Title: "First", Hook: "Hook", Reason: "Reason"},
		{StartWordIndex: 1, EndWordIndexExclusive: 3, Title: "Overlap", Hook: "Hook", Reason: "Reason"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func assertCurationState(t *testing.T, ctx context.Context, pool *sql.DB, id, status string, attempts, candidates int) {
	t.Helper()
	var actual string
	var claims, count int
	if err := pool.QueryRowContext(ctx, `
		SELECT status, attempts,
		    (SELECT COUNT(*) FROM media_curation_candidates WHERE asset_id = $1)
		FROM media_curation_jobs WHERE asset_id = $1
	`, id).Scan(&actual, &claims, &count); err != nil ||
		actual != status || claims != attempts || count != candidates {
		t.Fatalf("state %s: got %s/%d/%d want %s/%d/%d: %v",
			id, actual, claims, count, status, attempts, candidates, err)
	}
}

func TestCurationStoreLifecycle(t *testing.T) {
	ctx, pool := curationStoreDatabase(t)
	store := curation.SQLStore{DB: pool}
	t.Run("claim, derive timings and atomically publish stable proposals", func(t *testing.T) {
		c, data := claimCurationFixture(t, ctx, pool, "curation-ready")
		if c.Model != curation.Model || c.PromptVersion != curation.PromptVersion || c.UserID == "" {
			t.Fatal("missing trusted snapshot")
		}
		var lease bool
		if err := pool.QueryRowContext(ctx, `
			SELECT lease_until > clock_timestamp() + INTERVAL '170 seconds'
			    AND lease_until <= clock_timestamp() + INTERVAL '180 seconds'
			FROM media_curation_jobs WHERE asset_id = $1
		`, c.AssetID).Scan(&lease); err != nil || !lease {
			t.Fatal("claim lease differs from approved three minutes")
		}
		if _, err := store.Claim(ctx, c.AssetID, "duplicate"); !errors.Is(err, curation.ErrBusy) {
			t.Fatal("live duplicate claim accepted")
		}
		pub := curationPublication(t, c, data)
		wrong := c
		wrong.Token = "stale"
		if err := store.Finish(ctx, wrong, nil, "CURATION_TIMEOUT", true); !errors.Is(err, curation.ErrStale) {
			t.Fatal("stale token accepted")
		}
		if err := store.Finish(ctx, c, &curation.Publication{}, "", false); !errors.Is(err, curation.ErrResult) {
			t.Fatal("unvalidated publication accepted")
		}
		forged := c
		forged.MinDurationMs++
		forgedPub := curationPublication(t, forged, data)
		if err := store.Finish(ctx, forged, forgedPub, "", false); !errors.Is(err, curation.ErrStale) {
			t.Fatal("caller-replaced preferences accepted")
		}
		if err := store.Finish(ctx, c, forgedPub, "", false); !errors.Is(err, curation.ErrResult) {
			t.Fatal("publication for another claim accepted")
		}
		if err := store.Finish(ctx, c, pub, "", false); err != nil {
			t.Fatal(err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "ready", 1, 2)
		var id string
		var start, end int64
		if err := pool.QueryRowContext(ctx, `
			SELECT id, start_ms, end_ms FROM media_curation_candidates
			WHERE asset_id = $1 AND candidate_index = 0
		`, c.AssetID).Scan(&id, &start, &end); err != nil || id == "" || start != 0 || end != 700 {
			t.Fatal("timestamps were not derived from maximum selected word end")
		}
		if err := store.Finish(ctx, c, pub, "", false); !errors.Is(err, curation.ErrStale) {
			t.Fatal("duplicate finish was not fenced")
		}
		var sameID string
		if err := pool.QueryRowContext(ctx, `
			SELECT id FROM media_curation_candidates WHERE asset_id = $1 AND candidate_index = 0
		`, c.AssetID).Scan(&sameID); err != nil || sameID != id {
			t.Fatal("duplicate finish changed stable candidate ID")
		}
		if next, err := store.Claim(ctx, c.AssetID, "terminal"); err != nil || next.AssetID != "" {
			t.Fatal("terminal job reclaimed")
		}
	})
	t.Run("concurrent claims consume one attempt", func(t *testing.T) {
		const id = "curation-concurrent"
		enqueueCurationFixture(t, ctx, pool, id, false)
		var wg sync.WaitGroup
		results := make(chan curation.Claim, 8)
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := store.Claim(ctx, id, "same-token")
				results <- c
				errs <- err
			}()
		}
		wg.Wait()
		close(results)
		close(errs)
		winners := 0
		for c := range results {
			if c.AssetID != "" {
				winners++
			}
		}
		for err := range errs {
			if err != nil && !errors.Is(err, curation.ErrBusy) {
				t.Fatal(err)
			}
		}
		if winners != 1 {
			t.Fatalf("expected one winner, got %d", winners)
		}
		assertCurationState(t, ctx, pool, id, "running", 1, 0)
	})
	t.Run("empty proposals complete without candidates", func(t *testing.T) {
		const id = "curation-empty"
		data := enqueueCurationFixture(t, ctx, pool, id, true)
		c, err := store.Claim(ctx, id, "empty-claim")
		if err != nil {
			t.Fatal(err)
		}
		pub, err := curation.ValidateResult(c, data, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, c, pub, "", false); err != nil {
			t.Fatal(err)
		}
		assertCurationState(t, ctx, pool, id, "ready", 1, 0)
	})
	t.Run("missing and waiting jobs do not consume attempts", func(t *testing.T) {
		if c, err := store.Claim(ctx, "missing", "claim"); err != nil || c.AssetID != "" {
			t.Fatal("missing job claimed")
		}
		enqueueTranscriptFile(t, ctx, pool, "curation-waiting")
		if c, err := store.Claim(ctx, "curation-waiting", "claim"); err != nil || c.AssetID != "" {
			t.Fatal("unbound job claimed")
		}
		assertCurationState(t, ctx, pool, "curation-waiting", "pending", 0, 0)
	})
	t.Run("retries are delayed durable and capped", func(t *testing.T) {
		c, _ := claimCurationFixture(t, ctx, pool, "curation-retry")
		if err := store.Finish(ctx, c, nil, "CURATION_TIMEOUT", true); err != nil {
			t.Fatal(err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "pending", 1, 0)
		if early, err := store.Claim(ctx, c.AssetID, "early"); err != nil || early.AssetID != "" {
			t.Fatal("retry bypassed backoff")
		}
		var delayed bool
		if err := pool.QueryRowContext(ctx, `
			SELECT j.next_attempt_at > clock_timestamp() + INTERVAL '50 seconds'
			    AND e.next_attempt_at > clock_timestamp() + INTERVAL '50 seconds'
			    AND e.queue_name = 'curation'
			    AND e.payload = jsonb_build_object('assetId', j.asset_id)
			FROM media_curation_jobs j JOIN outbox_events e ON e.asset_id = j.asset_id
			WHERE j.asset_id = $1 AND e.dedupe_key LIKE '%:retry:1'
		`, c.AssetID).Scan(&delayed); err != nil || !delayed {
			t.Fatal("durable retry was not delayed/identifier-only")
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs SET next_attempt_at = clock_timestamp() WHERE asset_id = $1
		`, c.AssetID); err != nil {
			t.Fatal(err)
		}
		second, err := store.Claim(ctx, c.AssetID, "second")
		if err != nil || second.Attempts != 2 {
			t.Fatalf("second claim: %v", err)
		}
		if err := store.Finish(ctx, second, nil, "CURATION_TIMEOUT", true); err != nil {
			t.Fatal(err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "failed", 2, 0)
		if countCurationEvents(t, ctx, pool, c.AssetID) != 2 {
			t.Fatal("attempt cap created a third event")
		}
	})
	for _, code := range []string{"CURATION_INVALID_OUTPUT", "CURATION_PROVIDER_AUTH", "CURATION_PROVIDER_REJECTED", "secret-provider-body"} {
		t.Run("permanent/"+code, func(t *testing.T) {
			id := "curation-permanent-" + strings.ToLower(strings.ReplaceAll(code, "_", "-"))
			c, _ := claimCurationFixture(t, ctx, pool, id)
			if err := store.Finish(ctx, c, nil, code, true); err != nil {
				t.Fatal(err)
			}
			assertCurationState(t, ctx, pool, id, "failed", 1, 0)
			if countCurationEvents(t, ctx, pool, id) != 1 {
				t.Fatal("permanent failure retried")
			}
			var stored string
			if err := pool.QueryRowContext(ctx, `
				SELECT failure_code FROM media_curation_jobs WHERE asset_id = $1
			`, id).Scan(&stored); err != nil || (code == "secret-provider-body" && stored != "CURATION_FAILED") {
				t.Fatal("raw failure code persisted")
			}
		})
	}
	for _, moment := range []string{"before-claim", "before-finish"} {
		t.Run("source changed/"+moment, func(t *testing.T) {
			id := "curation-source-" + moment
			data := enqueueCurationFixture(t, ctx, pool, id, false)
			var c curation.Claim
			if moment == "before-finish" {
				var err error
				c, err = store.Claim(ctx, id, "source-claim")
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.ExecContext(ctx, `
				UPDATE media_assets SET user_id = NULL WHERE id = $1
			`, id); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			if moment == "before-claim" {
				if c, err := store.Claim(ctx, id, "source-claim"); err != nil || c.AssetID != "" {
					t.Fatal("unowned source claimed")
				}
			} else {
				attempts = 1
				if err := store.Finish(ctx, c, curationPublication(t, c, data), "", false); err != nil {
					t.Fatal(err)
				}
			}
			assertCurationState(t, ctx, pool, id, "failed", attempts, 0)
		})
	}
}

func TestCurationStoreAtomicity(t *testing.T) {
	ctx, pool := curationStoreDatabase(t)
	store := curation.SQLStore{DB: pool}
	t.Run("candidate batch rollback", func(t *testing.T) {
		c, data := claimCurationFixture(t, ctx, pool, "curation-batch-rollback")
		pub := curationPublication(t, c, data)
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE media_curation_candidates
			ADD CONSTRAINT reject_second_fixture CHECK (
			    asset_id <> 'curation-batch-rollback' OR candidate_index <> 1
			)
		`); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, c, pub, "", false); !errors.Is(err, curation.ErrDatabase) {
			t.Fatalf("expected safe DB error: %v", err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "running", 1, 0)
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE media_curation_candidates DROP CONSTRAINT reject_second_fixture
		`); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, c, pub, "", false); err != nil {
			t.Fatal(err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "ready", 1, 2)
	})
	t.Run("retry event failure rolls back outcome", func(t *testing.T) {
		c, _ := claimCurationFixture(t, ctx, pool, "curation-retry-rollback")
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE outbox_events ADD CONSTRAINT reject_retry_fixture CHECK (
			    dedupe_key NOT LIKE 'curate:curation-retry-rollback:%:retry:%'
			)
		`); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, c, nil, "CURATION_TIMEOUT", true); !errors.Is(err, curation.ErrDatabase) {
			t.Fatal("retry outbox failure was not returned safely")
		}
		assertCurationState(t, ctx, pool, c.AssetID, "running", 1, 0)
		if countCurationEvents(t, ctx, pool, c.AssetID) != 1 {
			t.Fatal("failed transaction left retry work")
		}
	})
	t.Run("retry dedupe collision is not delivery", func(t *testing.T) {
		c, _ := claimCurationFixture(t, ctx, pool, "curation-retry-collision")
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO outbox_events (
			    id, asset_id, task_type, queue_name, payload, dedupe_key
			)
			VALUES ('retry-collision', $1, 'wrong-task', 'wrong-queue', '{}', $2)
		`, c.AssetID, "curate:"+c.AssetID+":"+c.TranscriptSHA256+":retry:1"); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, c, nil, "CURATION_TIMEOUT", true); !errors.Is(err, curation.ErrConflict) {
			t.Fatalf("retry collision accepted: %v", err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "running", 1, 0)
	})
	t.Run("canceled operations preserve the claim", func(t *testing.T) {
		c, data := claimCurationFixture(t, ctx, pool, "curation-cancel")
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := store.Finish(canceled, c, curationPublication(t, c, data), "", false); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled finish: %v", err)
		}
		if _, err := store.Claim(canceled, c.AssetID, "canceled"); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled claim: %v", err)
		}
		if _, err := store.Recover(canceled, 100); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled recovery: %v", err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "running", 1, 0)
		for _, limit := range []int{0, 101} {
			if _, err := store.Recover(ctx, limit); !errors.Is(err, curation.ErrConfiguration) {
				t.Fatal("invalid recovery bound accepted")
			}
		}
	})
	t.Run("expiry during inserts fences entire publication", func(t *testing.T) {
		c, data := claimCurationFixture(t, ctx, pool, "curation-lease-expiry")
		pub := curationPublication(t, c, data)
		if _, err := pool.ExecContext(ctx, `
			CREATE FUNCTION delay_curation_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    IF NEW.asset_id = 'curation-lease-expiry' THEN
			        PERFORM pg_sleep(0.3);
			    END IF;
			    RETURN NEW;
			END;
			$$;
			CREATE TRIGGER delay_curation_fixture BEFORE INSERT ON media_curation_candidates
			FOR EACH ROW EXECUTE FUNCTION delay_curation_fixture();
			UPDATE media_curation_jobs
			SET lease_until = clock_timestamp() + INTERVAL '200 milliseconds'
			WHERE asset_id = 'curation-lease-expiry'
		`); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, c, pub, "", false); !errors.Is(err, curation.ErrStale) {
			t.Fatalf("lease expiry during transaction did not fence publication: %v", err)
		}
		assertCurationState(t, ctx, pool, c.AssetID, "running", 1, 0)
	})
}
