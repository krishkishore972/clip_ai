package migrations

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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/pressly/goose/v3"
)

// Opt-in integration test. Use a direct/session PostgreSQL connection to a test
// database, not a transaction pooler. Verify isolation before applying any SQL.
func TestMediaAssetsMigration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to test migrations in a disposable schema")
	}
	admin, err := db.Connect(databaseURL)
	if err != nil {
		t.Fatal("test database connection failed; details omitted to protect credentials")
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "migration_test_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("cannot create disposable migration schema; check test database permissions")
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		// This exact schema was randomly generated and created by this test.
		if _, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("could not clean up test-owned schema %s", schema)
		}
	})
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	// Every pooled connection targets only the test-owned schema. No public fallback.
	cfg.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = pool.Close() })
	var currentSchema string
	if err := pool.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatal("refusing migration test: connection is not isolated to the test-owned schema")
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, Files)
	if err != nil {
		t.Fatal(err)
	}
	up, err := provider.UpTo(ctx, 1)
	if err != nil || len(up) != 1 || up[0].Source.Version != 1 {
		t.Fatalf("initial Up: results=%v error=%v", up, err)
	}
	if again, err := provider.UpTo(ctx, 1); err != nil || len(again) != 0 {
		t.Fatalf("repeat Up should be a no-op: %v", err)
	}

	_, err = pool.ExecContext(ctx, `INSERT INTO media_assets
		(id, storage_bucket, storage_key, declared_content_type, size_bytes)
		VALUES ('test-asset', 'test-bucket', 'originals/test.mp4', 'video/mp4', 1024)`)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var duration, width, height, fpsNum, fpsDen sql.NullInt64
	var filename, videoCodec, audioCodec sql.NullString
	var validated sql.NullTime
	var created, updated time.Time
	err = pool.QueryRowContext(ctx, `SELECT status, original_filename, duration_ms, width, height,
		fps_num, fps_den, video_codec, audio_codec, validated_at, created_at, updated_at
		FROM media_assets WHERE id = 'test-asset'`).Scan(
		&status, &filename, &duration, &width, &height, &fpsNum, &fpsDen,
		&videoCodec, &audioCodec, &validated, &created, &updated)
	if err != nil {
		t.Fatal(err)
	}
	if status != "uploaded_unvalidated" || filename.Valid || duration.Valid || width.Valid || height.Valid ||
		fpsNum.Valid || fpsDen.Valid || videoCodec.Valid || audioCodec.Valid || validated.Valid ||
		created.IsZero() || updated.IsZero() {
		t.Fatal("new upload must have timestamps, unvalidated status, and no invented probe metadata")
	}

	for _, tc := range []struct {
		name string
		set  string
	}{
		{"empty ID", "id = ''"},
		{"empty bucket", "storage_bucket = ''"},
		{"empty key", "storage_key = ''"},
		{"empty declared MIME", "declared_content_type = ''"},
		{"zero size", "size_bytes = 0"},
		{"negative duration", "duration_ms = -1"},
		{"zero width", "width = 0"},
		{"negative height", "height = -1"},
		{"unknown status", "status = 'transcribed'"},
		{"missing FPS denominator", "fps_num = 30"},
		{"missing FPS numerator", "fps_den = 1"},
		{"zero FPS", "fps_num = 0, fps_den = 1"},
		{"negative FPS", "fps_num = 30, fps_den = -1"},
		{"empty codec", "video_codec = ''"},
		{"ready without probe", "status = 'ready'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.ExecContext(ctx, "UPDATE media_assets SET "+tc.set+" WHERE id = 'test-asset'")
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("expected check violation, got %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		id   string
		key  string
	}{
		{"duplicate ID", "test-asset", "originals/other.mp4"},
		{"duplicate storage location", "other-asset", "originals/test.mp4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.ExecContext(ctx, `INSERT INTO media_assets
				(id, storage_bucket, storage_key, declared_content_type, size_bytes)
				VALUES ($1, 'test-bucket', $2, 'video/mp4', 1024)`, tc.id, tc.key)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
				t.Fatalf("expected uniqueness violation, got %v", err)
			}
		})
	}
	for _, state := range []string{"validating", "rejected", "validation_failed"} {
		if _, err := pool.ExecContext(ctx, "UPDATE media_assets SET status = $1 WHERE id = 'test-asset'", state); err != nil {
			t.Fatalf("valid state rejected: %v", err)
		}
	}
	// A silent video is valid: audio_codec stays NULL. FPS is an exact rational.
	_, err = pool.ExecContext(ctx, `UPDATE media_assets SET status = 'ready', duration_ms = 1000,
		width = 1920, height = 1080, fps_num = 30000, fps_den = 1001,
		video_codec = 'h264', validated_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = 'test-asset'`)
	if err != nil {
		t.Fatal(err)
	}
	var num, den int
	if err := pool.QueryRowContext(ctx, "SELECT fps_num, fps_den FROM media_assets WHERE id = 'test-asset'").Scan(&num, &den); err != nil || num != 30000 || den != 1001 {
		t.Fatalf("rational FPS was not preserved: %v", err)
	}
	down, err := provider.Down(ctx)
	if err != nil || down == nil || down.Source.Version != 1 {
		t.Fatalf("Down in test-owned schema: %v", err)
	}
	var table sql.NullString
	if err := pool.QueryRowContext(ctx, "SELECT to_regclass('media_assets')::text").Scan(&table); err != nil || table.Valid {
		t.Fatalf("Down did not remove test table: %v", err)
	}
	if reapplied, err := provider.UpTo(ctx, 1); err != nil || len(reapplied) != 1 {
		t.Fatalf("Up after Down: %v", err)
	}
	var count int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM media_assets").Scan(&count); err != nil || count != 0 {
		t.Fatalf("reapplied migration did not create an empty table: %v", err)
	}
	t.Run("users and asset ownership", func(t *testing.T) { testIdentityAndOwnership(t, ctx, pool, provider) })
}

// Read-only verification after explicitly migrating the application database.
func TestMediaAssetsApplied(t *testing.T) {
	databaseURL := os.Getenv("VERIFY_MIGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set VERIFY_MIGRATION_DATABASE_URL to verify an applied migration")
	}
	pool, err := db.Connect(databaseURL)
	if err != nil {
		t.Fatal("verification database connection failed; details omitted to protect credentials")
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version int64
	if err := pool.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil || version < 1 {
		t.Fatalf("expected applied migration version 1: %v", err)
	}
	// Resolving the columns verifies the actual table, not just Goose bookkeeping.
	rows, err := pool.QueryContext(ctx, `SELECT id, storage_bucket, storage_key, original_filename,
		declared_content_type, size_bytes, status, duration_ms, width, height, fps_num, fps_den,
		video_codec, audio_codec, failure_code, created_at, updated_at, validated_at
		FROM media_assets LIMIT 0`)
	if err != nil {
		t.Fatal("media_assets is missing or its columns do not match migration 1")
	}
	defer rows.Close()
}
