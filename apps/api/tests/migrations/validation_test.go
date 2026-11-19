package migrations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/pressly/goose/v3"
)

// Called after direct ownership tests, in their isolated disposable schema.
func testValidationWorkerStore(t *testing.T, ctx context.Context, pool *sql.DB, provider *goose.Provider) {
	t.Helper()
	if results, err := provider.UpTo(ctx, 5); err != nil || len(results) != 1 {
		t.Fatalf("validation Up: %v", err)
	}
	if results, err := provider.UpTo(ctx, 5); err != nil || len(results) != 0 {
		t.Fatalf("repeat validation Up: %v", err)
	}
	// The additive rollback is safe only with no outstanding claim.
	if down, err := provider.Down(ctx); err != nil || down.Source.Version != 5 {
		t.Fatalf("validation Down: %v", err)
	}
	if _, err := provider.UpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}

	var owner string
	if err := pool.QueryRowContext(ctx, "SELECT user_id FROM media_assets WHERE id = 'owned'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	insert := func(id string) {
		t.Helper()
		const query = `
			INSERT INTO media_assets (
				id,
				user_id,
				storage_bucket,
				storage_key,
				declared_content_type,
				size_bytes
			)
			VALUES ($1, $2, 'validation-fixture', $1, 'video/mp4', 1024)
		`
		if _, err := pool.ExecContext(ctx, query, id, owner); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	store := job.SQLValidationStore{DB: pool}
	claim := func(id, token string) job.ValidationAsset {
		t.Helper()
		a, err := store.Claim(ctx, id, token, time.Minute)
		if err != nil || a.ID != id {
			t.Fatalf("claim %s: %+v %v", id, a, err)
		}
		return a
	}
	expire := func(id string) {
		exec(`
			UPDATE media_assets
			SET validation_lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE id = $1
		`, id)
	}
	for _, id := range []string{"missing", "legacy", "existing-asset"} {
		if a, err := store.Claim(ctx, id, "skip", time.Minute); err != nil || a.ID != "" {
			t.Fatalf("did not skip %s: %v", id, err)
		}
	}
	insert("validation-ready")
	a := claim("validation-ready", "first")
	if a.Attempts != 1 || a.Bucket != "validation-fixture" || a.Key != a.ID || a.SizeBytes != 1024 {
		t.Fatal("claim lost trusted asset fields")
	}
	if _, err := store.Claim(ctx, a.ID, "busy", time.Minute); !errors.Is(err, job.ErrValidationBusy) {
		t.Fatalf("live claim: %v", err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("rollback accepted an active claim")
	}
	metadata := media.Metadata{
		Container: "mp4", SizeBytes: 1024, DurationMs: 1000,
		Video: []media.VideoStream{{Index: 0, Codec: "h264", Width: 1920, Height: 1080, FPS: media.Rational{Num: 30000, Den: 1001}, RotationDegrees: 90}},
		Audio: []media.AudioStream{{Index: 1, Codec: "aac", SampleRate: 48000, Channels: 2}},
	}
	ready := job.ValidationOutcome{Status: "ready", Metadata: &metadata}
	expire(a.ID)
	if err := store.Finish(ctx, a, ready); !errors.Is(err, job.ErrValidationStale) {
		t.Fatalf("expired finish: %v", err)
	}
	b := claim(a.ID, "replacement")
	if b.Attempts != 2 {
		t.Fatal("replacement lost durable attempt count")
	}
	if err := store.Finish(ctx, a, ready); !errors.Is(err, job.ErrValidationStale) {
		t.Fatalf("old token finish: %v", err)
	}
	if err := store.Finish(ctx, b, ready); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	var bucket, key, user, video, audio string
	var duration, width, height, num, den int
	var validated bool
	const inspectReady = `
		SELECT
			probed_metadata,
			storage_bucket,
			storage_key,
			user_id,
			duration_ms,
			width,
			height,
			fps_num,
			fps_den,
			video_codec,
			audio_codec,
			validated_at IS NOT NULL AND validation_claim_token IS NULL AND validation_lease_until IS NULL
		FROM media_assets
		WHERE id = $1
	`
	if err := pool.QueryRowContext(ctx, inspectReady, b.ID).Scan(&encoded, &bucket, &key, &user, &duration, &width, &height, &num, &den, &video, &audio, &validated); err != nil {
		t.Fatal(err)
	}
	var decoded media.Metadata
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Video) != 1 || decoded.Video[0].RotationDegrees != 90 || len(decoded.Audio) != 1 || bucket != b.Bucket || key != b.Key || user != owner || duration != 1000 || width != 1920 || height != 1080 || num != 30000 || den != 1001 || video != "h264" || audio != "aac" || !validated {
		t.Fatal("ready metadata or immutable fields changed")
	}
	if a, err := store.Claim(ctx, b.ID, "terminal", time.Minute); err != nil || a.ID != "" {
		t.Fatal("ready asset was reclaimed")
	}

	insert("validation-crash")
	old := claim("validation-crash", "crashed")
	expire(old.ID)
	if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
		t.Fatalf("recovery: %d %v", n, err)
	}
	if n, err := store.Recover(ctx, 100); err != nil || n != 0 {
		t.Fatalf("repeat recovery: %d %v", n, err)
	}
	if err := store.Finish(ctx, old, ready); !errors.Is(err, job.ErrValidationStale) {
		t.Fatal("recovered token was not fenced")
	}
	var count int
	const inspectEvent = `
		SELECT COUNT(*) FROM outbox_events
		WHERE asset_id = $1
			AND dedupe_key = 'validate:validation-crash:recovery:1'
			AND task_type = 'media:validate'
			AND queue_name = 'validation'
			AND payload = '{"assetId":"validation-crash"}'::jsonb
			AND dispatched_at IS NULL
	`
	if err := pool.QueryRowContext(ctx, inspectEvent, old.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovery event: %d %v", count, err)
	}
	for attempt := 2; attempt <= 4; attempt++ {
		current := claim(old.ID, fmt.Sprintf("attempt-%d", attempt))
		if current.Attempts != attempt {
			t.Fatal("durable attempts did not increase")
		}
		if attempt < 4 {
			if err := store.Finish(ctx, current, job.ValidationOutcome{Status: "uploaded_unvalidated", Code: "PROBE_FAILED"}); err != nil {
				t.Fatal(err)
			}
		} else {
			expire(old.ID)
			if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
				t.Fatalf("exhausted recovery: %d %v", n, err)
			}
		}
	}
	var status, code string
	if err := pool.QueryRowContext(ctx, `SELECT status, failure_code FROM media_assets WHERE id = $1`, old.ID).Scan(&status, &code); err != nil || status != "validation_failed" || code != "VALIDATION_ATTEMPTS_EXHAUSTED" {
		t.Fatalf("attempt exhaustion: %s %s %v", status, code, err)
	}
	if a, err := store.Claim(ctx, old.ID, "fifth", time.Minute); err != nil || a.ID != "" {
		t.Fatal("fifth attempt acquired")
	}

	for _, terminal := range []string{"rejected", "validation_failed"} {
		id := "validation-" + terminal
		insert(id)
		current := claim(id, "terminal-token")
		if err := store.Finish(ctx, current, job.ValidationOutcome{Status: terminal, Code: "INVALID_MEDIA"}); err != nil {
			t.Fatal(err)
		}
		if a, err := store.Claim(ctx, id, "terminal-retry", time.Minute); err != nil || a.ID != "" {
			t.Fatal("terminal failure was reclaimed")
		}
	}
	insert("validation-pending-exhausted")
	exec("UPDATE media_assets SET validation_attempts = 4 WHERE id = 'validation-pending-exhausted'")
	if a, err := store.Claim(ctx, "validation-pending-exhausted", "fifth", time.Minute); err != nil || a.ID != "" {
		t.Fatal("exhausted pending asset was reclaimed")
	}
	if err := pool.QueryRowContext(ctx, "SELECT status FROM media_assets WHERE id = 'validation-pending-exhausted'").Scan(&status); err != nil || status != "validation_failed" {
		t.Fatal("exhausted pending asset was not made terminal")
	}

	insert("validation-legacy-state")
	exec("UPDATE media_assets SET status = 'validating' WHERE id = 'validation-legacy-state'")
	exec("UPDATE media_assets SET status = 'validating' WHERE id = 'legacy'")
	if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
		t.Fatalf("NULL lease recovery/unowned skip: %d %v", n, err)
	}
	exec("UPDATE media_assets SET status = 'uploaded_unvalidated' WHERE id = 'legacy'")
	// Retry bookkeeping and its backup outbox event must commit atomically.
	insert("validation-retry-atomic")
	retryAsset := claim("validation-retry-atomic", "retry-token")
	exec(`ALTER TABLE outbox_events ADD CONSTRAINT test_validation_retry_fail CHECK (asset_id <> 'validation-retry-atomic')`)
	retryOutcome := job.ValidationOutcome{Status: "uploaded_unvalidated", Code: "DOWNLOAD_FAILED"}
	if err := store.Finish(ctx, retryAsset, retryOutcome); !errors.Is(err, job.ErrValidationDatabase) {
		t.Fatalf("retry event failure was not propagated: %v", err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT status FROM media_assets WHERE id = $1", retryAsset.ID).Scan(&status); err != nil || status != "validating" {
		t.Fatal("retry state committed without durable event")
	}
	exec(`ALTER TABLE outbox_events DROP CONSTRAINT test_validation_retry_fail`)
	if err := store.Finish(ctx, retryAsset, retryOutcome); err != nil {
		t.Fatal(err)
	}
	var retryEvent string
	var future bool
	if err := pool.QueryRowContext(ctx, `
		SELECT id, next_attempt_at > clock_timestamp()
		FROM outbox_events
		WHERE asset_id = $1 AND dedupe_key = 'validate:validation-retry-atomic:retry:1'
	`, retryAsset.ID).Scan(&retryEvent, &future); err != nil || !future {
		t.Fatalf("delayed retry backup missing: %v", err)
	}
	exec("UPDATE outbox_events SET dispatched_at = clock_timestamp() WHERE id = $1", retryEvent)
	candidates, err := store.ArchiveCandidates(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.AssetID == retryAsset.ID && candidate.EventID == retryEvent {
			found = true
		}
	}
	if !found {
		t.Fatal("archived-task reconciliation candidate missing")
	}
	// A newer durable event fences reconciliation of an older archived task.
	exec(`
		INSERT INTO outbox_events (id, asset_id, task_type, queue_name, payload, dedupe_key)
		VALUES ('newer-retry-event', $1, 'media:validate', 'validation', jsonb_build_object('assetId', $1::text), 'newer-retry-event')
	`, retryAsset.ID)
	if err := store.FailArchived(ctx, job.ValidationDelivery{AssetID: retryAsset.ID, EventID: retryEvent}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT status FROM media_assets WHERE id = $1", retryAsset.ID).Scan(&status); err != nil || status != "uploaded_unvalidated" {
		t.Fatal("old archive overwrote newer delivery")
	}
	newClaim := claim(retryAsset.ID, "newer-live-token")
	if err := store.FailArchived(ctx, job.ValidationDelivery{AssetID: retryAsset.ID, EventID: "newer-retry-event"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT status FROM media_assets WHERE id = $1", retryAsset.ID).Scan(&status); err != nil || status != "validating" {
		t.Fatal("archive overwrote live claim")
	}
	if err := store.Finish(ctx, newClaim, ready); err != nil {
		t.Fatal(err)
	}
	// All claims have finished or recovered, so the rollback guard now permits Down.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
}
