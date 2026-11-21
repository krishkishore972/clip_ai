package migrations_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/upload"
	"github.com/krishkishore972/clip_ai/migrations"
	"github.com/pressly/goose/v3"
)

func processTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated test database")
	}
	admin, err := db.Connect(databaseURL)
	if err != nil {
		t.Fatal("test database connection failed")
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "process_claim_test_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("cannot create isolated test schema")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cannot remove test-owned schema")
		}
	})
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	cfg.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = pool.Close() })
	var currentSchema string
	if err := pool.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatal("refusing test: schema isolation is not active")
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func TestProcessClaimFreshUpload(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	principal, err := (auth.Store{DB: pool}).Resolve(ctx, "https://fixture.clerk.accounts.dev", "claim-user")
	if err != nil {
		t.Fatal(err)
	}
	assets := upload.SQLAssets{DB: pool}
	completed := upload.CompletedAsset{
		ID: "fresh-process-claim", Bucket: "fixture-bucket", Key: "fixture.mp4",
		ContentType: "video/mp4", SizeBytes: 1024,
	}
	if err := assets.SaveCompleted(auth.WithPrincipal(ctx, principal), completed); err != nil {
		t.Fatal(err)
	}
	var lease sql.NullTime
	if err := pool.QueryRowContext(ctx, `
		SELECT validation_lease_until
		FROM media_assets
		WHERE id = $1
	`, completed.ID).Scan(&lease); err != nil || lease.Valid {
		t.Fatalf("fresh upload must have a NULL validation lease: valid=%v, error=%v", lease.Valid, err)
	}
	store := job.SQLProcessStore{DB: pool}
	claimed, err := store.Claim(ctx, completed.ID, "fixture-token", 91*time.Minute)
	if err != nil {
		t.Fatalf("claim fresh completed upload: %v", err)
	}
	if claimed.ID != completed.ID || claimed.Attempts != 1 || claimed.Token != "fixture-token" || claimed.OutputID == "" {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	var status, prepStatus string
	var attempts, prepAttempts int
	if err := pool.QueryRowContext(ctx, `
		SELECT
			a.status,
			a.validation_attempts,
			p.status,
			p.attempts
		FROM media_assets a
		JOIN media_preparations p ON p.asset_id = a.id
		WHERE a.id = $1
	`, completed.ID).Scan(&status, &attempts, &prepStatus, &prepAttempts); err != nil {
		t.Fatal(err)
	}
	if status != "validating" || attempts != 1 || prepStatus != "preparing" || prepAttempts != 1 {
		t.Fatalf("unexpected persisted claim: %s/%d %s/%d", status, attempts, prepStatus, prepAttempts)
	}
	if _, err := store.Claim(ctx, completed.ID, "duplicate-token", 91*time.Minute); !errors.Is(err, job.ErrProcessBusy) {
		t.Fatalf("live claim should remain protected: %v", err)
	}
}
