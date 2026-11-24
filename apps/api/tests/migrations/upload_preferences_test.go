package migrations_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

func TestCompletedUploadPreferences(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	owner, err := (auth.Store{DB: pool}).Resolve(ctx, "https://fixture.clerk.accounts.dev", "preferences-owner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := (auth.Store{DB: pool}).Resolve(ctx, "https://fixture.clerk.accounts.dev", "preferences-other")
	if err != nil {
		t.Fatal(err)
	}
	ownerCtx := auth.WithPrincipal(ctx, owner)
	records := upload.SQLAssets{DB: pool}
	preferences := upload.ClipPreferences{CandidateCount: 5, MinDurationMs: 15000, MaxDurationMs: 60000}
	asset := upload.CompletedAsset{
		ID: "preferences-asset", Bucket: "fixture-bucket", Key: "preferences/original.mp4",
		Filename: "original.mp4", ContentType: "video/mp4", SizeBytes: 1024,
		ClipPreferences: &preferences,
	}
	if err := records.SaveCompleted(ownerCtx, asset); err != nil {
		t.Fatal(err)
	}
	assertAssetEventCounts(t, ctx, pool, asset.ID, 1, 1)
	var stored upload.ClipPreferences
	var state string
	if err := pool.QueryRowContext(ctx, `
		SELECT candidate_count, min_duration_ms, max_duration_ms, status
		FROM media_curation_jobs
		WHERE asset_id = $1
	`, asset.ID).Scan(&stored.CandidateCount, &stored.MinDurationMs, &stored.MaxDurationMs, &state); err != nil ||
		stored != preferences || state != "pending" {
		t.Fatal("signed upload preferences not persisted as pending")
	}
	t.Run("concurrent completion retries", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- records.SaveCompleted(ownerCtx, asset)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		assertAssetEventCounts(t, ctx, pool, asset.ID, 1, 1)
	})
	t.Run("different preferences conflict without modification", func(t *testing.T) {
		for _, p := range []upload.ClipPreferences{
			{CandidateCount: 4, MinDurationMs: 15000, MaxDurationMs: 60000},
			{CandidateCount: 5, MinDurationMs: 14000, MaxDurationMs: 60000},
			{CandidateCount: 5, MinDurationMs: 15000, MaxDurationMs: 59000},
		} {
			changed := asset
			changed.ClipPreferences = &p
			if err := records.SaveCompleted(ownerCtx, changed); !errors.Is(err, upload.ErrConflict) {
				t.Fatalf("changed preferences did not conflict: %v", err)
			}
		}
	})
	t.Run("ownership is checked before preference conflicts", func(t *testing.T) {
		if err := records.SaveCompleted(auth.WithPrincipal(ctx, other), asset); !errors.Is(err, upload.ErrNotFound) {
			t.Fatal("cross-user completion not hidden")
		}
		if err := records.SaveCompleted(ctx, asset); !errors.Is(err, upload.ErrUnauthenticated) {
			t.Fatal("unauthenticated completion accepted")
		}
		insertCurationAsset(t, ctx, pool, "preferences-null-owner")
		legacy := asset
		legacy.ID, legacy.Key = "preferences-null-owner", "preferences-null-owner/original.mp4"
		if err := records.SaveCompleted(ownerCtx, legacy); !errors.Is(err, upload.ErrNotFound) {
			t.Fatal("NULL-owned asset was adopted")
		}
	})
	t.Run("retry preserves finished curation and candidates", func(t *testing.T) {
		_, err := pool.ExecContext(ctx, `
			UPDATE media_curation_jobs
			SET
			    status = 'ready',
			    attempts = 1,
			    transcript_bucket = 'fixture-bucket',
			    transcript_key = 'private/transcript.json',
			    transcript_sha256 = $2,
			    transcript_size_bytes = 1024,
			    transcript_word_count = 100,
			    source_duration_ms = 60000,
			    started_at = CURRENT_TIMESTAMP,
			    completed_at = CURRENT_TIMESTAMP
			WHERE asset_id = $1
		`, asset.ID, strings.Repeat("a", 64))
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
			VALUES ('preserved-candidate', $1, 0, 0, 10, 0, 15000, 'Title', 'Hook', 'Reason')
		`, asset.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := records.SaveCompleted(ownerCtx, asset); err != nil {
			t.Fatal(err)
		}
		var attempts, candidates int
		if err := pool.QueryRowContext(ctx, `
			SELECT
			    j.status,
			    j.attempts,
			    (
			        SELECT COUNT(*)
			        FROM media_curation_candidates c
			        WHERE c.asset_id = j.asset_id
			    )
			FROM media_curation_jobs j
			WHERE j.asset_id = $1
		`, asset.ID).Scan(&state, &attempts, &candidates); err != nil ||
			state != "ready" || attempts != 1 || candidates != 1 {
			t.Fatal("completion retry reset curation")
		}
	})
	t.Run("legacy completion skips curation", func(t *testing.T) {
		legacy := asset
		legacy.ID, legacy.Key = "preferences-legacy", "preferences-legacy/original.mp4"
		legacy.ClipPreferences = nil
		if err := records.SaveCompleted(ownerCtx, legacy); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM media_curation_jobs WHERE asset_id = $1
		`, legacy.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("legacy upload received invented curation work")
		}
	})
	for _, stage := range []string{"curation", "outbox"} {
		t.Run(stage+" failure rolls back entire completion", func(t *testing.T) {
			rejected := asset
			rejected.ID, rejected.Key = "preferences-rejected", "preferences-rejected/original.mp4"
			table := "media_curation_jobs"
			if stage == "outbox" {
				table = "outbox_events"
			}
			// Only fixed test-owned identifiers and literals are concatenated.
			if _, err := pool.ExecContext(ctx,
				"ALTER TABLE "+table+" ADD CONSTRAINT reject_preferences_fixture CHECK (asset_id <> 'preferences-rejected')",
			); err != nil {
				t.Fatal(err)
			}
			if err := records.SaveCompleted(ownerCtx, rejected); err == nil {
				t.Fatal("forced persistence failure unexpectedly succeeded")
			}
			assertAssetEventCounts(t, ctx, pool, rejected.ID, 0, 0)
			var count int
			if err := pool.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM media_curation_jobs WHERE asset_id = $1
			`, rejected.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed completion left partial curation state")
			}
			if _, err := pool.ExecContext(ctx,
				"ALTER TABLE "+table+" DROP CONSTRAINT reject_preferences_fixture",
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}
