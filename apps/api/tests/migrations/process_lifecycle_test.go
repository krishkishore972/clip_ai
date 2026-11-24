package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

func claimProcessFixture(t *testing.T, ctx context.Context, pool *sql.DB, id string) job.ProcessAsset {
	t.Helper()
	principal, err := (auth.Store{DB: pool}).Resolve(ctx, "https://fixture.clerk.accounts.dev", "process-user")
	if err != nil {
		t.Fatal(err)
	}
	completed := upload.CompletedAsset{
		ID: id, Bucket: "fixture-bucket", Key: id + ".mp4",
		ContentType: "video/mp4", SizeBytes: 1024,
		ClipPreferences: upload.ClipPreferences{CandidateCount: 1, MinDurationMs: 1, MaxDurationMs: 1000},
	}
	if err := (upload.SQLAssets{DB: pool}).SaveCompleted(auth.WithPrincipal(ctx, principal), completed); err != nil {
		t.Fatal(err)
	}
	claimed, err := (job.SQLProcessStore{DB: pool}).Claim(ctx, id, id+"-token", 91*time.Minute)
	if err != nil || claimed.ID != id {
		t.Fatalf("claim fixture: %+v %v", claimed, err)
	}
	return claimed
}

func readyProcessOutcome(asset job.ProcessAsset) job.ProcessOutcome {
	return job.ProcessOutcome{
		AssetStatus: "ready",
		PrepStatus:  "ready",
		Metadata: &media.Metadata{
			Container: "mp4", SizeBytes: asset.SizeBytes, DurationMs: 1000,
			Video: []media.VideoStream{{
				Index: 0, Codec: "h264", Width: 160, Height: 96,
				FPS: media.Rational{Num: 24, Den: 1},
			}},
			Audio: []media.AudioStream{{
				Index: 1, Codec: "aac", SampleRate: 48000, Channels: 1,
			}},
		},
		Artifacts: &preparation.Artifacts{
			Bucket:         asset.Bucket,
			AudioKey:       "dev-uploads/" + asset.ID + "/derived/v1/" + asset.OutputID + "/audio.wav",
			AudioSHA256:    strings.Repeat("a", 64),
			SourceSHA256:   strings.Repeat("b", 64),
			AudioSizeBytes: 32044,
			AudioStartMs:   -7,
		},
	}
}

func assertProcessState(t *testing.T, ctx context.Context, pool *sql.DB, id, assetStatus, prepStatus string, attempts int) {
	t.Helper()
	const query = `
		SELECT
		    a.status,
		    p.status,
		    p.attempts,
		    a.validation_claim_token IS NULL,
		    a.validation_lease_until IS NULL,
		    p.claim_token IS NULL,
		    p.lease_until IS NULL,
		    p.output_id IS NULL
		FROM media_assets a
		JOIN media_preparations p ON p.asset_id = a.id
		WHERE a.id = $1
	`
	var gotAsset, gotPrep string
	var gotAttempts int
	var assetTokenNull, assetLeaseNull, prepTokenNull, prepLeaseNull, outputNull bool
	if err := pool.QueryRowContext(ctx, query, id).Scan(
		&gotAsset,
		&gotPrep,
		&gotAttempts,
		&assetTokenNull,
		&assetLeaseNull,
		&prepTokenNull,
		&prepLeaseNull,
		&outputNull,
	); err != nil {
		t.Fatal(err)
	}
	if gotAsset != assetStatus || gotPrep != prepStatus || gotAttempts != attempts {
		t.Fatalf("state = %s/%s/%d, want %s/%s/%d", gotAsset, gotPrep, gotAttempts, assetStatus, prepStatus, attempts)
	}
	released := assetStatus != "validating"
	if assetTokenNull != released || assetLeaseNull != released ||
		prepTokenNull != released || prepLeaseNull != released || outputNull != released {
		t.Fatalf("inconsistent claim release for %s", id)
	}
}

func TestProcessStoreFinishLifecycle(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	store := job.SQLProcessStore{DB: pool}
	t.Run("ready publication and owned status", func(t *testing.T) {
		asset := claimProcessFixture(t, ctx, pool, "publish-ready")
		if err := store.Finish(ctx, asset, readyProcessOutcome(asset)); err != nil {
			t.Fatal(err)
		}
		assertProcessState(t, ctx, pool, asset.ID, "ready", "ready", 1)
		var owner string
		var origin int64
		if err := pool.QueryRowContext(ctx, `
			SELECT a.user_id, p.audio_start_ms
			FROM media_assets a
			JOIN media_preparations p ON p.asset_id = a.id
			WHERE a.id = $1
		`, asset.ID).Scan(&owner, &origin); err != nil || origin != -7 {
			t.Fatalf("signed audio origin lost: %d %v", origin, err)
		}
		status, err := (preparation.SQLStore{DB: pool}).GetStatus(ctx, owner, asset.ID)
		if err != nil || status.Status != "ready" || status.PreparedAt == nil {
			t.Fatalf("owned status: %+v %v", status, err)
		}
		if _, err := (preparation.SQLStore{DB: pool}).GetStatus(ctx, "other-user", asset.ID); !errors.Is(err, preparation.ErrNotFound) {
			t.Fatalf("cross-user status accepted: %v", err)
		}
		if err := store.Finish(ctx, asset, readyProcessOutcome(asset)); !errors.Is(err, job.ErrProcessStale) {
			t.Fatalf("duplicate finish accepted: %v", err)
		}
	})
	t.Run("retry and stale fencing", func(t *testing.T) {
		asset := claimProcessFixture(t, ctx, pool, "retry-finish")
		retry := job.ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "DOWNLOAD_FAILED"}
		if err := store.Finish(ctx, asset, retry); err != nil {
			t.Fatal(err)
		}
		assertProcessState(t, ctx, pool, asset.ID, "uploaded_unvalidated", "pending", 1)
		var count int
		var delayed bool
		if err := pool.QueryRowContext(ctx, `
			SELECT COUNT(*), BOOL_AND(next_attempt_at > clock_timestamp())
			FROM outbox_events
			WHERE asset_id = $1
			    AND dedupe_key = 'process:' || $1 || ':retry:1'
		`, asset.ID).Scan(&count, &delayed); err != nil || count != 1 || !delayed {
			t.Fatalf("missing delayed retry: count=%d delayed=%t error=%v", count, delayed, err)
		}
		current, err := store.Claim(ctx, asset.ID, "retry-token", 91*time.Minute)
		if err != nil || current.Attempts != job.ProcessAttempts {
			t.Fatalf("second claim: %+v %v", current, err)
		}
		if err := store.Finish(ctx, asset, retry); !errors.Is(err, job.ErrProcessStale) {
			t.Fatalf("stale finish accepted: %v", err)
		}
		assertProcessState(t, ctx, pool, asset.ID, "validating", "preparing", 2)
		if err := store.Finish(ctx, current, job.ProcessOutcome{
			AssetStatus: "validation_failed",
			Code:        "DOWNLOAD_FAILED",
		}); err != nil {
			t.Fatal(err)
		}
		assertProcessState(t, ctx, pool, asset.ID, "validation_failed", "failed", 2)
	})
	t.Run("permanent rejection", func(t *testing.T) {
		asset := claimProcessFixture(t, ctx, pool, "reject-finish")
		if err := store.Finish(ctx, asset, job.ProcessOutcome{
			AssetStatus: "rejected", PrepStatus: "failed", Code: "INVALID_MEDIA",
		}); err != nil {
			t.Fatal(err)
		}
		assertProcessState(t, ctx, pool, asset.ID, "rejected", "failed", 1)
		var retries int
		if err := pool.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM outbox_events
			WHERE asset_id = $1 AND task_type = 'media:process'
		`, asset.ID).Scan(&retries); err != nil || retries != 0 {
			t.Fatalf("rejected source retried: %d %v", retries, err)
		}
	})
}

func TestProcessStoreRejectsInvalidPublication(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	store := job.SQLProcessStore{DB: pool}
	asset := claimProcessFixture(t, ctx, pool, "invalid-publication")
	for _, tc := range []struct {
		name   string
		mutate func(*job.ProcessOutcome)
	}{
		{name: "nil metadata", mutate: func(o *job.ProcessOutcome) { o.Metadata = nil }},
		{name: "empty streams", mutate: func(o *job.ProcessOutcome) { o.Metadata.Video = nil }},
		{name: "mismatched size", mutate: func(o *job.ProcessOutcome) { o.Metadata.SizeBytes++ }},
		{name: "nil artifacts", mutate: func(o *job.ProcessOutcome) { o.Artifacts = nil }},
		{name: "wrong bucket", mutate: func(o *job.ProcessOutcome) { o.Artifacts.Bucket = "other-bucket" }},
		{name: "original key", mutate: func(o *job.ProcessOutcome) { o.Artifacts.AudioKey = asset.Key }},
		{name: "invalid hash", mutate: func(o *job.ProcessOutcome) { o.Artifacts.AudioSHA256 = "bad" }},
		{name: "out-of-range origin", mutate: func(o *job.ProcessOutcome) { o.Artifacts.AudioStartMs = media.MaxAudioStartMs + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := readyProcessOutcome(asset)
			tc.mutate(&outcome)
			if err := store.Finish(ctx, asset, outcome); !errors.Is(err, job.ErrProcessArguments) {
				t.Fatalf("invalid publication accepted: %v", err)
			}
			assertProcessState(t, ctx, pool, asset.ID, "validating", "preparing", 1)
		})
	}
	if err := store.Finish(ctx, asset, readyProcessOutcome(asset)); err != nil {
		t.Fatalf("valid publication after invalid proposals: %v", err)
	}
}

func expireProcessFixture(t *testing.T, ctx context.Context, pool *sql.DB, id string, attempts int) {
	t.Helper()
	_, err := pool.ExecContext(ctx, `
		UPDATE media_assets
		SET validation_lease_until = clock_timestamp() - INTERVAL '1 minute'
		WHERE id = $1
	`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.ExecContext(ctx, `
		UPDATE media_preparations
		SET
		    attempts = $2,
		    lease_until = clock_timestamp() - INTERVAL '1 minute'
		WHERE asset_id = $1
	`, id, attempts)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessStoreRecoveryTransitions(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	store := job.SQLProcessStore{DB: pool}
	retry := claimProcessFixture(t, ctx, pool, "recover-retry")
	exhausted := claimProcessFixture(t, ctx, pool, "recover-exhausted")
	live := claimProcessFixture(t, ctx, pool, "recover-live")
	expireProcessFixture(t, ctx, pool, retry.ID, 1)
	expireProcessFixture(t, ctx, pool, exhausted.ID, job.ProcessAttempts)
	n, err := store.Recover(ctx, 100)
	if err != nil || n != 2 {
		t.Fatalf("recovery: count=%d error=%v", n, err)
	}
	assertProcessState(t, ctx, pool, retry.ID, "uploaded_unvalidated", "pending", 1)
	assertProcessState(t, ctx, pool, exhausted.ID, "validation_failed", "failed", job.ProcessAttempts)
	assertProcessState(t, ctx, pool, live.ID, "validating", "preparing", 1)
	var events int
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM outbox_events
		WHERE dedupe_key = 'process:' || $1 || ':recovery:1'
	`, retry.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("missing recovery event: %d %v", events, err)
	}
	if n, err := store.Recover(ctx, 100); err != nil || n != 0 {
		t.Fatalf("repeat recovery is not a no-op: %d %v", n, err)
	}
}

func TestProcessClaimExhaustionReleasesBothClaims(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	store := job.SQLProcessStore{DB: pool}
	asset := claimProcessFixture(t, ctx, pool, "claim-exhausted")
	expireProcessFixture(t, ctx, pool, asset.ID, job.ProcessAttempts)
	claimed, err := store.Claim(ctx, asset.ID, "next-token", 91*time.Minute)
	if err != nil || claimed.ID != "" {
		t.Fatalf("exhausted claim: %+v %v", claimed, err)
	}
	assertProcessState(t, ctx, pool, asset.ID, "validation_failed", "failed", job.ProcessAttempts)
}

func TestProcessFinishRollsBackBothRecords(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	store := job.SQLProcessStore{DB: pool}
	asset := claimProcessFixture(t, ctx, pool, "rollback-publication")
	// The constraint is isolated to this test-owned schema and fixture. Force
	// the second write to fail after the asset update would otherwise succeed.
	if _, err := pool.ExecContext(ctx, `
		ALTER TABLE media_preparations
		ADD CONSTRAINT fixture_block_ready CHECK (
		    asset_id <> 'rollback-publication' OR status <> 'ready'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, asset, readyProcessOutcome(asset)); !errors.Is(err, job.ErrProcessDatabase) {
		t.Fatalf("expected publication database error: %v", err)
	}
	assertProcessState(t, ctx, pool, asset.ID, "validating", "preparing", 1)
	if _, err := pool.ExecContext(ctx, `
		ALTER TABLE media_preparations
		DROP CONSTRAINT fixture_block_ready
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, asset, readyProcessOutcome(asset)); err != nil {
		t.Fatalf("publication did not recover after rollback: %v", err)
	}
	assertProcessState(t, ctx, pool, asset.ID, "ready", "ready", 1)
}
