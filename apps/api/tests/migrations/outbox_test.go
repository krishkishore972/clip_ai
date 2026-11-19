package migrations_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

// Called only after the parent migration test has verified schema isolation
// and applied all migrations to its disposable test-owned schema.
func testCompletedAssetOutbox(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	principal, err := (auth.Store{DB: pool}).Resolve(
		ctx,
		"https://fixture.clerk.accounts.dev",
		"outbox-owner",
	)
	if err != nil {
		t.Fatal(err)
	}
	ownerCtx := auth.WithPrincipal(ctx, principal)
	records := upload.SQLAssets{DB: pool}
	asset := upload.CompletedAsset{
		ID:          "outbox-asset",
		Bucket:      "fixture-bucket",
		Key:         "outbox/original.mp4",
		Filename:    "original.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
	}

	t.Run("atomic insert and concurrent retry deduplication", func(t *testing.T) {
		if err := records.SaveCompleted(ownerCtx, asset); err != nil {
			t.Fatal(err)
		}
		assertAssetEventCounts(t, ctx, pool, asset.ID, 1, 1)

		const inspectEventQuery = `
			SELECT
				id,
				asset_id,
				task_type,
				queue_name,
				payload ->> 'assetId',
				dedupe_key,
				dispatch_attempts,
				next_attempt_at,
				dispatched_at
			FROM outbox_events
			WHERE asset_id = $1
		`
		var eventID, assetID, taskType, queue, payloadAssetID, dedupeKey string
		var attempts int
		var nextAttempt time.Time
		var dispatched sql.NullTime
		err := pool.QueryRowContext(ctx, inspectEventQuery, asset.ID).Scan(
			&eventID,
			&assetID,
			&taskType,
			&queue,
			&payloadAssetID,
			&dedupeKey,
			&attempts,
			&nextAttempt,
			&dispatched,
		)
		if err != nil {
			t.Fatal(err)
		}
		rawID, err := hex.DecodeString(eventID)
		if err != nil || len(rawID) != 16 {
			t.Fatal("event ID must be a random 16-byte hex identifier")
		}
		if assetID != asset.ID ||
			taskType != "media:validate" ||
			queue != "validation" ||
			payloadAssetID != asset.ID ||
			dedupeKey != "validate:"+asset.ID+":initial" ||
			attempts != 0 ||
			nextAttempt.IsZero() ||
			dispatched.Valid {
			t.Fatal("initial validation event fields/defaults are incorrect")
		}

		const concurrency = 16
		var wg sync.WaitGroup
		errs := make(chan error, concurrency)
		for range concurrency {
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

		var afterID string
		const findEventIDQuery = `
			SELECT id
			FROM outbox_events
			WHERE asset_id = $1
		`
		if err := pool.QueryRowContext(ctx, findEventIDQuery, asset.ID).Scan(&afterID); err != nil || afterID != eventID {
			t.Fatal("completion retry changed the stable event ID")
		}
	})

	t.Run("retry preserves validated metadata and delivered event", func(t *testing.T) {
		const markValidatedQuery = `
			UPDATE media_assets
			SET
				status = 'ready',
				duration_ms = 1000,
				width = 1920,
				height = 1080,
				fps_num = 30000,
				fps_den = 1001,
				video_codec = 'h264',
				audio_codec = 'aac',
				validated_at = CURRENT_TIMESTAMP
			WHERE id = $1
		`
		const markDispatchedQuery = `
			UPDATE outbox_events
			SET
				dispatched_at = CURRENT_TIMESTAMP,
				dispatch_attempts = 2
			WHERE asset_id = $1
		`
		for _, query := range []string{markValidatedQuery, markDispatchedQuery} {
			if _, err := pool.ExecContext(ctx, query, asset.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := records.SaveCompleted(ownerCtx, asset); err != nil {
			t.Fatal(err)
		}
		const inspectPreservedQuery = `
			SELECT
				a.status,
				a.duration_ms,
				a.width,
				a.height,
				a.fps_num,
				a.fps_den,
				a.video_codec,
				a.audio_codec,
				a.validated_at,
				e.dispatched_at,
				e.dispatch_attempts
			FROM media_assets a
			JOIN outbox_events e
				ON e.asset_id = a.id
			WHERE a.id = $1
		`
		var status, video, audio string
		var duration, width, height, num, den, attempts int
		var validated, dispatched sql.NullTime
		if err := pool.QueryRowContext(ctx, inspectPreservedQuery, asset.ID).Scan(
			&status,
			&duration,
			&width,
			&height,
			&num,
			&den,
			&video,
			&audio,
			&validated,
			&dispatched,
			&attempts,
		); err != nil {
			t.Fatal(err)
		}
		if status != "ready" || duration != 1000 ||
			width != 1920 || height != 1080 ||
			num != 30000 || den != 1001 ||
			video != "h264" || audio != "aac" ||
			!validated.Valid || !dispatched.Valid || attempts != 2 {
			t.Fatal("completion retry reset validation metadata or dispatch state")
		}
		assertAssetEventCounts(t, ctx, pool, asset.ID, 1, 1)
	})

	t.Run("outbox failure rolls back new asset and retry recovers", func(t *testing.T) {
		rollbackAsset := asset
		rollbackAsset.ID = "outbox-rollback"
		rollbackAsset.Key = "outbox/rollback.mp4"
		const forceFailureQuery = `
			ALTER TABLE outbox_events
			ADD CONSTRAINT test_reject_outbox_asset
			CHECK (asset_id <> 'outbox-rollback')
		`
		const removeFailureQuery = `
			ALTER TABLE outbox_events
			DROP CONSTRAINT test_reject_outbox_asset
		`
		if _, err := pool.ExecContext(ctx, forceFailureQuery); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := pool.ExecContext(cleanupCtx, `
				ALTER TABLE outbox_events
				DROP CONSTRAINT IF EXISTS test_reject_outbox_asset
			`); err != nil {
				t.Error("could not remove test-only outbox constraint")
			}
		})
		err := records.SaveCompleted(ownerCtx, rollbackAsset)
		if err == nil || !strings.Contains(err.Error(), "persist validation outbox event") {
			t.Fatalf("expected outbox insertion failure, got %v", err)
		}
		assertAssetEventCounts(t, ctx, pool, rollbackAsset.ID, 0, 0)

		if _, err := pool.ExecContext(ctx, removeFailureQuery); err != nil {
			t.Fatal(err)
		}
		if err := records.SaveCompleted(ownerCtx, rollbackAsset); err != nil {
			t.Fatal(err)
		}
		assertAssetEventCounts(t, ctx, pool, rollbackAsset.ID, 1, 1)
	})

	t.Run("rejected completion cannot create an event", func(t *testing.T) {
		other, err := (auth.Store{DB: pool}).Resolve(
			ctx,
			"https://fixture.clerk.accounts.dev",
			"outbox-other-user",
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name  string
			ctx   context.Context
			asset upload.CompletedAsset
			want  error
		}{
			{"unauthenticated", ctx, asset, upload.ErrUnauthenticated},
			{"cross-user", auth.WithPrincipal(ctx, other), asset, upload.ErrNotFound},
			{"unknown user", auth.WithPrincipal(ctx, auth.Principal{UserID: "missing-user"}), asset, upload.ErrNotFound},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if err := records.SaveCompleted(tc.ctx, tc.asset); !errors.Is(err, tc.want) {
					t.Fatalf("expected %v, got %v", tc.want, err)
				}
				assertAssetEventCounts(t, ctx, pool, asset.ID, 1, 1)
			})
		}
		for _, mutate := range []func(*upload.CompletedAsset){
			func(a *upload.CompletedAsset) { a.Bucket = "different-bucket" },
			func(a *upload.CompletedAsset) { a.Key = "different-key" },
			func(a *upload.CompletedAsset) { a.Filename = "different.mp4" },
			func(a *upload.CompletedAsset) { a.ContentType = "video/quicktime" },
			func(a *upload.CompletedAsset) { a.SizeBytes++ },
		} {
			conflict := asset
			mutate(&conflict)
			if err := records.SaveCompleted(ownerCtx, conflict); !errors.Is(err, upload.ErrNotFound) {
				t.Fatalf("conflicting completion was accepted: %v", err)
			}
			assertAssetEventCounts(t, ctx, pool, asset.ID, 1, 1)
		}
		// An unauthenticated/new nonexistent user must create neither record.
		newAsset := asset
		newAsset.ID = "outbox-unauthorized"
		newAsset.Key = "outbox/unauthorized.mp4"
		for _, requestCtx := range []context.Context{
			ctx,
			auth.WithPrincipal(ctx, auth.Principal{UserID: "missing-user"}),
		} {
			if err := records.SaveCompleted(requestCtx, newAsset); err == nil {
				t.Fatal("unauthorized new completion was accepted")
			}
			assertAssetEventCounts(t, ctx, pool, newAsset.ID, 0, 0)
		}
	})
}

func assertAssetEventCounts(t *testing.T, ctx context.Context, pool *sql.DB, assetID string, wantAssets, wantEvents int) {
	t.Helper()
	const query = `
		SELECT
			(SELECT COUNT(*) FROM media_assets WHERE id = $1),
			(SELECT COUNT(*) FROM outbox_events WHERE asset_id = $1)
	`
	var assets, events int
	if err := pool.QueryRowContext(ctx, query, assetID).Scan(&assets, &events); err != nil {
		t.Fatal(err)
	}
	if assets != wantAssets || events != wantEvents {
		t.Fatalf("asset %s: got %d assets / %d events; want %d / %d", assetID, assets, events, wantAssets, wantEvents)
	}
}
