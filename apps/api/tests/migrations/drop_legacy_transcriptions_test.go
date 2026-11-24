package migrations_test

import "testing"

func TestDropLegacyTranscriptionsMigration(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	insertCurationAsset(t, ctx, pool, "legacy-drop-fixture")
	// This legacy row is deliberately disposable test data in a guarded schema.
	_, err := pool.ExecContext(ctx, `
		INSERT INTO media_transcriptions (asset_id)
		VALUES ('legacy-drop-fixture');

		INSERT INTO outbox_events (
		    id,
		    asset_id,
		    task_type,
		    queue_name,
		    payload,
		    dedupe_key
		)
		VALUES (
		    'old-event',
		    'legacy-drop-fixture',
		    'media:transcribe',
		    'processing',
		    '{"assetId":"legacy-drop-fixture"}',
		    'legacy-drop-event'
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	up, err := provider.UpTo(ctx, 10)
	if err != nil || len(up) != 1 || up[0].Source.Version != 10 {
		t.Fatalf("legacy drop Up failed: %v", err)
	}
	var removed, retained bool
	if err := pool.QueryRowContext(ctx, `
		SELECT
		    to_regclass('media_transcriptions') IS NULL
		        AND to_regclass('outbox_events_transcribe_asset_idx') IS NULL,
		    to_regclass('media_transcript_files') IS NOT NULL
		        AND to_regclass('media_curation_jobs') IS NOT NULL
		        AND to_regclass('media_curation_candidates') IS NOT NULL
	`).Scan(&removed, &retained); err != nil || !removed || !retained {
		t.Fatalf("unexpected objects removed/retained: %v", err)
	}
	var count int
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM outbox_events WHERE id = 'old-event'
	`).Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy drop unexpectedly deleted outbox events")
	}
	if again, err := provider.UpTo(ctx, 10); err != nil || len(again) != 0 {
		t.Fatal("repeat Up must be a no-op")
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("irreversible Down must refuse")
	}
	if again, err := provider.UpTo(ctx, 10); err != nil || len(again) != 0 {
		t.Fatal("refused Down changed migration history")
	}
}

func TestDropLegacyTranscriptionsAlreadyAbsent(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `
		DROP TABLE media_transcriptions;
		DROP INDEX outbox_events_transcribe_asset_idx;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatalf("manually removed development objects must be supported: %v", err)
	}
}

func TestDropLegacyTranscriptionsRefusesUnexpectedDependencies(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `
		CREATE VIEW legacy_dependency_fixture AS
		SELECT asset_id FROM media_transcriptions
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 10); err == nil {
		t.Fatal("drop must not cascade through unexpected dependencies")
	}
	var preserved bool
	if err := pool.QueryRowContext(ctx, `
		SELECT
		    to_regclass('media_transcriptions') IS NOT NULL
		        AND to_regclass('legacy_dependency_fixture') IS NOT NULL
		        AND to_regclass('outbox_events_transcribe_asset_idx') IS NOT NULL
	`).Scan(&preserved); err != nil || !preserved {
		t.Fatal("failed drop damaged existing objects")
	}
}
