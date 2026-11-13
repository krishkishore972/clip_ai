package migrations

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/upload"
	"github.com/pressly/goose/v3"
)

func testIdentityAndOwnership(t *testing.T, ctx context.Context, pool *sql.DB, provider *goose.Provider) {
	_, err := pool.ExecContext(ctx, `INSERT INTO media_assets (id,storage_bucket,storage_key,declared_content_type,size_bytes)
		VALUES ('legacy','fixture-bucket','legacy.mp4','video/mp4',1024)`)
	if err != nil {
		t.Fatal(err)
	}
	results, err := provider.Up(ctx)
	if err != nil || len(results) != 1 || results[0].Source.Version != 2 {
		t.Fatalf("migration 2 Up: %v", err)
	}
	if results, err := provider.Up(ctx); err != nil || len(results) != 0 {
		t.Fatalf("repeat Up: %v", err)
	}
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
	for _, table := range []string{"users", "workspaces", "workspace_members"} {
		var count int
		if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: expected exactly one row: %v", table, err)
		}
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
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM media_assets WHERE id='owned'").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate completion created duplicate assets")
	}
	for _, p := range []auth.Principal{other, {UserID: other.UserID, WorkspaceID: owner.WorkspaceID}} {
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
	var workspace sql.NullString
	if err := pool.QueryRowContext(ctx, "SELECT workspace_id FROM media_assets WHERE id='legacy'").Scan(&workspace); err != nil || workspace.Valid {
		t.Fatal("migration or persistence assigned legacy ownership")
	}
	down, err := provider.Down(ctx)
	if err != nil || down.Source.Version != 2 {
		t.Fatalf("ownership Down in test schema: %v", err)
	}
	var table sql.NullString
	if err := pool.QueryRowContext(ctx, "SELECT to_regclass('users')::text").Scan(&table); err != nil || table.Valid {
		t.Fatal("ownership Down did not remove users")
	}
	if results, err := provider.Up(ctx); err != nil || len(results) != 1 {
		t.Fatalf("ownership reapply: %v", err)
	}
}
