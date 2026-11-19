package migrations_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/db"
)

// Explicitly opted-in, read-only verification; never applies SQL migrations or
// reads user rows. Normal tests skip it without the separate verification URL.
func TestValidationStatusSchemaApplied(t *testing.T) {
	url := os.Getenv("VERIFY_MIGRATION_DATABASE_URL")
	if url == "" {
		t.Skip("set VERIFY_MIGRATION_DATABASE_URL for read-only application schema verification")
	}
	pool, err := db.Connect(url)
	if err != nil {
		t.Fatal("schema verification connection failed; connection details omitted")
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version int64
	if err := pool.QueryRowContext(ctx, "SELECT MAX(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil || version < 5 {
		t.Fatal("validation migration 00005 is not applied")
	}
	const query = `
		SELECT
			user_id,
			status,
			failure_code,
			validation_attempts,
			validation_claim_token,
			validation_lease_until,
			probed_metadata,
			updated_at,
			validated_at
		FROM media_assets
		LIMIT 0
	`
	rows, err := pool.QueryContext(ctx, query)
	if err != nil {
		t.Fatal("validation status schema is unavailable")
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal("validation status schema verification failed")
	}
}
