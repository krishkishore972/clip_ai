package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/pressly/goose/v3"
)

func testPreparationStore(t *testing.T, ctx context.Context, pool *sql.DB, provider *goose.Provider) {
	t.Helper()
	if up, err := provider.UpTo(ctx, 6); err != nil || len(up) != 1 {
		t.Fatalf("preparation Up: %v", err)
	}
	if up, err := provider.UpTo(ctx, 6); err != nil || len(up) != 0 {
		t.Fatalf("repeat preparation Up: %v", err)
	}
	if err := preparation.CheckSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if down, err := provider.Down(ctx); err != nil || down.Source.Version != 6 {
		t.Fatalf("empty preparation Down: %v", err)
	}
	if _, err := provider.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := pool.QueryRowContext(ctx, `
		SELECT user_id
		FROM media_assets
		WHERE id = 'owned'
	`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	metadata := media.Metadata{Container: "mp4", SizeBytes: 1024, DurationMs: 1000, Video: []media.VideoStream{{Index: 0, Codec: "h264", Width: 1920, Height: 1080, FPS: media.Rational{Num: 30, Den: 1}}}, Audio: []media.AudioStream{{Index: 1, Codec: "aac", SampleRate: 48000, Channels: 2}}}
	validate := func(id string, enabled bool) {
		t.Helper()
		exec(`
		INSERT INTO media_assets (
		    id,
		    user_id,
		    storage_bucket,
		    storage_key,
		    declared_content_type,
		    size_bytes
		)
		VALUES ($1, $2, 'fixture-bucket', $1, 'video/mp4', 1024)
	`, id, owner)
		s := job.SQLValidationStore{DB: pool, PrepareValidated: enabled}
		c, err := s.Claim(ctx, id, "validate-"+id, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, c, job.ValidationOutcome{Status: "ready", Metadata: &metadata}); err != nil {
			t.Fatal(err)
		}
	}
	// Force the preparation event to fail and verify validation READY and the
	// pending preparation both roll back under the same still-live claim.
	exec(`
		INSERT INTO media_assets (
		    id,
		    user_id,
		    storage_bucket,
		    storage_key,
		    declared_content_type,
		    size_bytes
		)
		VALUES ('prep-atomic', $1, 'fixture-bucket', 'prep-atomic', 'video/mp4', 1024)
	`, owner)
	atomicStore := job.SQLValidationStore{DB: pool, PrepareValidated: true}
	atomicClaim, err := atomicStore.Claim(ctx, "prep-atomic", "atomic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	exec(`
		ALTER TABLE outbox_events
		ADD CONSTRAINT test_preparation_event_failure
		CHECK (task_type <> 'media:prepare')
	`)
	if err := atomicStore.Finish(ctx, atomicClaim, job.ValidationOutcome{Status: "ready", Metadata: &metadata}); !errors.Is(err, job.ErrValidationDatabase) {
		t.Fatalf("forced failure: %v", err)
	}
	if n := count(`
		SELECT COUNT(*)
		FROM media_preparations
		WHERE asset_id = 'prep-atomic'
	`); n != 0 {
		t.Fatal("failed validation commit retained preparation")
	}
	if n := count(`
		SELECT COUNT(*)
		FROM media_assets
		WHERE id = 'prep-atomic'
		    AND status = 'validating'
		    AND validation_claim_token = 'atomic-token'
	`); n != 1 {
		t.Fatal("failed preparation event committed validation READY")
	}
	exec(`
		ALTER TABLE outbox_events
		DROP CONSTRAINT test_preparation_event_failure
	`)
	if err := atomicStore.Finish(ctx, atomicClaim, job.ValidationOutcome{Status: "ready", Metadata: &metadata}); err != nil {
		t.Fatal(err)
	}
	if err := atomicStore.Finish(ctx, atomicClaim, job.ValidationOutcome{Status: "ready", Metadata: &metadata}); !errors.Is(err, job.ErrValidationStale) {
		t.Fatalf("stale validation: %v", err)
	}
	validate("prep-auto", true)
	if n := count(`
		SELECT COUNT(*)
		FROM media_preparations
		WHERE asset_id = 'prep-auto'
	`); n != 1 {
		t.Fatalf("auto prep count %d", n)
	}
	if n := count(`
		SELECT COUNT(*)
		FROM outbox_events
		WHERE dedupe_key = 'prepare:prep-auto:v1'
		    AND payload = '{"assetId":"prep-auto"}'::jsonb
	`); n != 1 {
		t.Fatalf("auto outbox count %d", n)
	}
	store := preparation.SQLStore{DB: pool}
	for _, id := range []string{"missing", "legacy"} {
		if _, err := store.GetStatus(ctx, owner, id); !errors.Is(err, preparation.ErrNotFound) {
			t.Fatalf("inaccessible %s: %v", id, err)
		}
	}
	if _, err := store.GetStatus(ctx, "wrong-owner", "prep-auto"); !errors.Is(err, preparation.ErrNotFound) {
		t.Fatalf("cross-owner: %v", err)
	}
	exec(`
		INSERT INTO media_assets (
		    id,
		    user_id,
		    storage_bucket,
		    storage_key,
		    declared_content_type,
		    size_bytes
		)
		VALUES ('prep-not-ready', $1, 'fixture-bucket', 'prep-not-ready', 'video/mp4', 1024)
	`, owner)
	if _, err := store.Ensure(ctx, owner, "prep-not-ready"); !errors.Is(err, preparation.ErrNotReady) {
		t.Fatalf("non-ready ensure: %v", err)
	}
	validate("prep-legacy", false)
	status, err := store.GetStatus(ctx, owner, "prep-legacy")
	if err != nil || status.Status != "not_prepared" {
		t.Fatalf("legacy: %+v %v", status, err)
	}
	if n := count(`
		SELECT COUNT(*)
		FROM media_preparations
		WHERE asset_id = 'prep-legacy'
	`); n != 0 {
		t.Fatal("GET created prep")
	}
	for range 3 {
		if status, err := store.Ensure(ctx, owner, "prep-legacy"); err != nil || status.Status != "pending" {
			t.Fatalf("ensure: %+v %v", status, err)
		}
	}
	if n := count(`
		SELECT COUNT(*)
		FROM outbox_events
		WHERE dedupe_key = 'prepare:prep-legacy:v1'
	`); n != 1 {
		t.Fatalf("ensure dedupe: %d", n)
	}
	c, err := store.Claim(ctx, "prep-auto", "token-one", 91*time.Minute)
	if err != nil || c.AssetID != "prep-auto" || c.Attempts != 1 || c.OutputID == c.Token {
		t.Fatalf("claim %+v %v", c, err)
	}
	if _, err := store.Claim(ctx, c.AssetID, "duplicate", time.Minute); !errors.Is(err, preparation.ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("Down accepted active preparation")
	}
	exec(`
		ALTER TABLE outbox_events
		ADD CONSTRAINT test_preparation_retry_failure
		CHECK (task_type <> 'media:prepare') NOT VALID
	`)
	if err := store.Finish(ctx, c, preparation.Outcome{Status: "pending", Code: "PREPARATION_TIMEOUT"}); !errors.Is(err, preparation.ErrDatabase) {
		t.Fatalf("retry event failure: %v", err)
	}
	if n := count(`
		SELECT COUNT(*)
		FROM media_preparations
		WHERE asset_id = $1
		    AND status = 'preparing'
		    AND claim_token = $2
	`, c.AssetID, c.Token); n != 1 {
		t.Fatal("failed retry event released claim")
	}
	exec(`
		ALTER TABLE outbox_events
		DROP CONSTRAINT test_preparation_retry_failure
	`)
	exec(`
		UPDATE media_preparations
		SET lease_until = clock_timestamp() - INTERVAL '1 second'
		WHERE asset_id = $1
	`, c.AssetID)
	if err := store.Finish(ctx, c, preparation.Outcome{Status: "pending", Code: "PREPARATION_TIMEOUT"}); !errors.Is(err, preparation.ErrStale) {
		t.Fatalf("expired finish %v", err)
	}
	if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
		t.Fatalf("recovery %d %v", n, err)
	}
	d, err := store.Claim(ctx, c.AssetID, "token-two", time.Minute)
	if err != nil || d.Attempts != 2 {
		t.Fatalf("second claim %+v %v", d, err)
	}
	if err := store.Finish(ctx, c, preparation.Outcome{Status: "failed", Code: "PREPARATION_FAILED"}); !errors.Is(err, preparation.ErrStale) {
		t.Fatalf("stale fence %v", err)
	}
	if err := store.Finish(ctx, d, preparation.Outcome{Status: "pending", Code: "PREPARATION_TIMEOUT"}); err != nil {
		t.Fatal(err)
	}
	status, err = store.Ensure(ctx, owner, c.AssetID)
	if err != nil || status.Status != "failed" || status.Attempts != 2 {
		t.Fatalf("exhaustion/reset %+v %v", status, err)
	}
	if c, err := store.Claim(ctx, c.AssetID, "third", time.Minute); err != nil || c.AssetID != "" {
		t.Fatalf("third attempt %+v %v", c, err)
	}
	good, err := store.Claim(ctx, "prep-legacy", "success-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "dev-uploads/" + good.AssetID + "/derived/v1/" + good.OutputID + "/"
	artifacts := &preparation.Artifacts{Bucket: good.Bucket, AudioKey: prefix + "audio.wav", AudioSHA256: strings.Repeat("b", 64), SourceSHA256: strings.Repeat("c", 64), AudioSizeBytes: 200}
	if err := store.Finish(ctx, good, preparation.Outcome{Status: "ready", Artifacts: artifacts}); err != nil {
		t.Fatal(err)
	}
	status, err = store.Ensure(ctx, owner, good.AssetID)
	if err != nil || status.Status != "ready" || status.PreparedAt == nil {
		t.Fatalf("ready %+v %v", status, err)
	}
	if _, err := pool.ExecContext(ctx, `
		UPDATE media_preparations
		SET audio_key = NULL
		WHERE asset_id = $1
	`, good.AssetID); err == nil {
		t.Fatal("ready allowed incomplete artifacts")
	}
	if _, err := pool.ExecContext(ctx, `
		UPDATE media_preparations
		SET audio_size_bytes = 240000001
		WHERE asset_id = $1
	`, good.AssetID); err == nil {
		t.Fatal("ready allowed oversized artifacts")
	}
	// Retry backup is durable and delayed, not a dependency on queue retries.
	if n := count(`
		SELECT COUNT(*)
		FROM outbox_events
		WHERE dedupe_key = 'prepare:prep-auto:v1:retry:1'
		    AND next_attempt_at > created_at
	`); n != 1 {
		t.Fatalf("delayed retry outbox: %d", n)
	}
	exec(`
		UPDATE outbox_events
		SET dispatched_at = clock_timestamp()
		WHERE dedupe_key = 'prepare:prep-atomic:v1'
	`)
	deliveries, err := store.ArchiveCandidates(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var delivery preparation.Delivery
	for _, d := range deliveries {
		if d.AssetID == "prep-atomic" {
			delivery = d
		}
	}
	if delivery.EventID == "" {
		t.Fatal("missing archive candidate")
	}
	if err := store.FailArchived(ctx, preparation.Delivery{AssetID: delivery.AssetID, EventID: "stale-event"}); err != nil {
		t.Fatal(err)
	}
	if status, err := store.GetStatus(ctx, owner, delivery.AssetID); err != nil || status.Status != "pending" {
		t.Fatalf("stale archive changed pending: %+v %v", status, err)
	}
	if err := store.FailArchived(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if status, err := store.GetStatus(ctx, owner, delivery.AssetID); err != nil || status.Status != "failed" || status.FailureCode == nil || *status.FailureCode != "PREPARATION_RETRIES_EXHAUSTED" {
		t.Fatalf("archive failure: %+v %v", status, err)
	}
	// Successful publication does not change immutable source metadata or status.
	if n := count(`
		SELECT COUNT(*)
		FROM media_assets
		WHERE id = $1
		    AND status = 'ready'
		    AND storage_key = $1
		    AND size_bytes = 1024
		    AND duration_ms = 1000
	`, good.AssetID); n != 1 {
		t.Fatal("preparation changed source")
	}
	t.Run("joined validation preparation pipeline", func(t *testing.T) {
		testPreparationPipeline(t, ctx, pool)
	})
}
