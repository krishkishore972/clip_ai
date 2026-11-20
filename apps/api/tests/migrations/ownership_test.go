package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/upload"
	"github.com/pressly/goose/v3"
)

func testIdentityAndOwnership(t *testing.T, ctx context.Context, pool *sql.DB, provider *goose.Provider) {
	const insertLegacyAssetQuery = `
		INSERT INTO media_assets (
			id,
			storage_bucket,
			storage_key,
			declared_content_type,
			size_bytes
		)
		VALUES (
			'legacy',
			'fixture-bucket',
			'legacy.mp4',
			'video/mp4',
			1024
		)
	`

	_, err := pool.ExecContext(ctx, insertLegacyAssetQuery)
	if err != nil {
		t.Fatal(err)
	}
	results, err := provider.UpTo(ctx, 2)
	if err != nil || len(results) != 1 || results[0].Source.Version != 2 {
		t.Fatalf("migration 2 Up: %v", err)
	}
	if results, err := provider.UpTo(ctx, 2); err != nil || len(results) != 0 {
		t.Fatalf("repeat Up: %v", err)
	}
	// Exercise the historical schema's rollback before applying its successor.
	down, err := provider.Down(ctx)
	if err != nil || down.Source.Version != 2 {
		t.Fatalf("ownership Down in test schema: %v", err)
	}
	var table sql.NullString
	if err := pool.QueryRowContext(ctx, "SELECT to_regclass('users')::text").Scan(&table); err != nil || table.Valid {
		t.Fatal("ownership Down did not remove users")
	}
	if results, err := provider.UpTo(ctx, 2); err != nil || len(results) != 1 {
		t.Fatalf("ownership reapply: %v", err)
	}
	testDirectOwnershipMigration(t, ctx, pool, provider)

	store := auth.Store{DB: pool}
	const concurrency = 16
	identities := make(chan auth.Principal, concurrency)
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := store.Resolve(ctx, "https://fixture.clerk.accounts.dev", "user_a")
			if err != nil {
				errs <- err
				return
			}
			identities <- p
		}()
	}
	wg.Wait()
	close(identities)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var owner auth.Principal
	for p := range identities {
		if owner.UserID == "" {
			owner = p
		}
		if p != owner {
			t.Fatal("concurrent requests created multiple identities")
		}
	}
	const countIdentityQuery = `
		SELECT COUNT(*)
		FROM users
		WHERE clerk_issuer = $1
			AND clerk_subject = $2
	`
	var identitiesCount int
	if err := pool.QueryRowContext(ctx, countIdentityQuery, "https://fixture.clerk.accounts.dev", "user_a").Scan(&identitiesCount); err != nil || identitiesCount != 1 {
		t.Fatalf("expected exactly one user for the identity: %v", err)
	}
	other, err := store.Resolve(ctx, "https://fixture.clerk.accounts.dev", "user_b")
	if err != nil {
		t.Fatal(err)
	}
	otherIssuer, err := store.Resolve(ctx, "https://different.clerk.accounts.dev", "user_a")
	if err != nil || otherIssuer == owner {
		t.Fatal("issuer is not part of stable identity")
	}
	assets := upload.SQLAssets{DB: pool}
	ownedCtx := auth.WithPrincipal(ctx, owner)
	in := upload.CompletedAsset{ID: "owned", Bucket: "fixture-bucket", Key: "original.mp4", Filename: "test.mp4", ContentType: "video/mp4", SizeBytes: 1024}
	for range 2 {
		if err := assets.SaveCompleted(ownedCtx, in); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	const countOwnedAssetsQuery = `
		SELECT COUNT(*)
		FROM media_assets
		WHERE id = 'owned'
	`

	if err := pool.QueryRowContext(ctx, countOwnedAssetsQuery).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate completion created duplicate assets")
	}
	for _, p := range []auth.Principal{other, otherIssuer} {
		if err := assets.SaveCompleted(auth.WithPrincipal(ctx, p), in); !errors.Is(err, upload.ErrNotFound) {
			t.Fatalf("cross-user write accepted: %v", err)
		}
	}
	if err := assets.SaveCompleted(ctx, in); !errors.Is(err, upload.ErrUnauthenticated) {
		t.Fatal("unauthenticated write accepted")
	}
	in.ID = "legacy"
	in.Key = "legacy.mp4"
	if err := assets.SaveCompleted(ownedCtx, in); !errors.Is(err, upload.ErrNotFound) {
		t.Fatal("adopted an unowned legacy asset")
	}
	var userID sql.NullString
	const findLegacyOwnerQuery = `
		SELECT user_id
		FROM media_assets
		WHERE id = 'legacy'
	`

	if err := pool.QueryRowContext(ctx, findLegacyOwnerQuery).Scan(&userID); err != nil || userID.Valid {
		t.Fatal("migration or persistence assigned legacy ownership")
	}
	// Immutable details still cannot be changed through completion retries.
	in.ID = "owned"
	in.Key = "different.mp4"
	if err := assets.SaveCompleted(ownedCtx, in); !errors.Is(err, upload.ErrNotFound) {
		t.Fatal("completion changed an existing asset's immutable fields")
	}
	in.ID = "unknown-user-asset"
	if err := assets.SaveCompleted(auth.WithPrincipal(ctx, auth.Principal{UserID: "not-in-db"}), in); !errors.Is(err, upload.ErrNotFound) {
		t.Fatal("nonexistent application user created an asset")
	}
	t.Run("completed asset outbox transaction", func(t *testing.T) {
		testCompletedAssetOutbox(t, ctx, pool)
	})
	t.Run("outbox dispatcher", func(t *testing.T) {
		testOutboxDispatcher(t, ctx, pool)
	})
	t.Run("validation persistence and recovery", func(t *testing.T) {
		testValidationWorkerStore(t, ctx, pool, provider)
	})
	t.Run("validation queue pipeline", func(t *testing.T) {
		testValidationPipeline(t, ctx, pool)
	})
	t.Run("owned media status", func(t *testing.T) {
		testMediaStatus(t, ctx, pool)
	})
	t.Run("preparation persistence", func(t *testing.T) {
		testPreparationStore(t, ctx, pool, provider)
	})
}

func testDirectOwnershipMigration(t *testing.T, ctx context.Context, pool *sql.DB, provider *goose.Provider) {
	t.Helper()
	// Set up an old-schema owned asset. Missing membership must fail closed.
	const insertUserQuery = `
		INSERT INTO users (id, clerk_issuer, clerk_subject)
		VALUES ('existing-user', 'https://fixture.clerk.accounts.dev', 'existing-subject')
	`
	const insertWorkspaceQuery = `
		INSERT INTO workspaces (id, personal_user_id)
		VALUES ('old-personal', 'existing-user')
	`
	const insertOwnedAssetQuery = `
		INSERT INTO media_assets (
			id,
			workspace_id,
			storage_bucket,
			storage_key,
			original_filename,
			declared_content_type,
			size_bytes,
			status,
			duration_ms,
			width,
			height,
			fps_num,
			fps_den,
			video_codec,
			audio_codec,
			validated_at
		)
		VALUES (
			'existing-asset',
			'old-personal',
			'fixture-bucket',
			'existing.mp4',
			'existing.mp4',
			'video/mp4',
			1024,
			'ready',
			1000,
			1920,
			1080,
			30000,
			1001,
			'h264',
			'aac',
			CURRENT_TIMESTAMP
		)
	`
	for _, query := range []string{insertUserQuery, insertWorkspaceQuery, insertOwnedAssetQuery} {
		if _, err := pool.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	const insertOutboxQuery = `
		INSERT INTO outbox_events (
			id,
			asset_id,
			task_type,
			queue_name,
			payload,
			dedupe_key
		)
		VALUES (
			'existing-event',
			'existing-asset',
			'media:validate',
			'validation',
			'{"assetId":"existing-asset"}',
			'validate:existing-asset:initial'
		)
	`
	if _, err := pool.ExecContext(ctx, insertOutboxQuery); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 4); err == nil {
		t.Fatal("inconsistent ownership did not abort migration")
	}
	const inspectOwnerColumnsQuery = `
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = CURRENT_SCHEMA()
			AND table_name = 'media_assets'
			AND column_name = $1
	`
	var count int
	if err := pool.QueryRowContext(ctx, inspectOwnerColumnsQuery, "workspace_id").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed migration did not preserve the old schema")
	}
	if err := pool.QueryRowContext(ctx, inspectOwnerColumnsQuery, "user_id").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed migration left partial schema changes")
	}
	const insertMembershipQuery = `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ('old-personal', 'existing-user', 'owner')
	`
	if _, err := pool.ExecContext(ctx, insertMembershipQuery); err != nil {
		t.Fatal(err)
	}
	if results, err := provider.UpTo(ctx, 4); err != nil || len(results) != 1 || results[0].Source.Version != 4 {
		t.Fatalf("direct user ownership Up: %v", err)
	}
	if results, err := provider.UpTo(ctx, 4); err != nil || len(results) != 0 {
		t.Fatalf("repeat direct ownership Up: %v", err)
	}
	const inspectMigratedAssetQuery = `
		SELECT
			a.user_id,
			a.status,
			a.storage_key,
			a.duration_ms,
			a.width,
			a.height,
			a.fps_num,
			a.fps_den,
			a.audio_codec,
			e.asset_id
		FROM media_assets a
		JOIN outbox_events e
			ON e.asset_id = a.id
		WHERE a.id = 'existing-asset'
			AND e.id = 'existing-event'
	`
	var user, status, key, audio, asset string
	var duration, width, height, num, den int
	if err := pool.QueryRowContext(ctx, inspectMigratedAssetQuery).Scan(
		&user,
		&status,
		&key,
		&duration,
		&width,
		&height,
		&num,
		&den,
		&audio,
		&asset,
	); err != nil {
		t.Fatal(err)
	}
	if user != "existing-user" || status != "ready" || key != "existing.mp4" || duration != 1000 || width != 1920 || height != 1080 || num != 30000 || den != 1001 || audio != "aac" || asset != "existing-asset" {
		t.Fatal("migration changed existing ownership, metadata or outbox references")
	}
	const invalidOwnerQuery = `
		UPDATE media_assets
		SET user_id = 'missing-user'
		WHERE id = 'existing-asset'
	`
	_, err := pool.ExecContext(ctx, invalidOwnerQuery)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.Code != "23503" {
		t.Fatal("direct ownership foreign key accepted a nonexistent user")
	}
	const deleteOwnerQuery = `
		DELETE FROM users
		WHERE id = 'existing-user'
	`
	_, err = pool.ExecContext(ctx, deleteOwnerQuery)
	if !errors.As(err, &constraint) || constraint.Code != "23503" {
		t.Fatal("deleting an asset owner was not restricted")
	}
	resolved, err := (auth.Store{DB: pool}).Resolve(ctx, "https://fixture.clerk.accounts.dev", "existing-subject")
	if err != nil || resolved.UserID != "existing-user" {
		t.Fatal("existing user identity changed during migration")
	}
	for _, name := range []string{"workspaces", "workspace_members"} {
		var table sql.NullString
		if err := pool.QueryRowContext(ctx, "SELECT to_regclass($1)::text", name).Scan(&table); err != nil || table.Valid {
			t.Fatalf("obsolete table %s still exists", name)
		}
	}
	if err := pool.QueryRowContext(ctx, inspectOwnerColumnsQuery, "workspace_id").Scan(&count); err != nil || count != 0 {
		t.Fatal("obsolete ownership column still exists")
	}
	// The only supported rollback is restoring a verified backup.
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("irreversible migration allowed a destructive rollback")
	}
	if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_assets").Scan(&count); err != nil || count != 2 {
		t.Fatal("refused rollback changed asset data")
	}
}
