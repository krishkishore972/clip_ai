package db

import (
	"os"
	"testing"
)

func TestConnectRejectsInvalidDSN(t *testing.T) {
	t.Parallel()
	pool, err := Connect("postgres://example:fake@localhost:invalid-port/database")
	if err == nil || pool != nil {
		if pool != nil {
			_ = pool.Close()
		}
		t.Fatal("invalid DSN must fail without returning a pool")
	}
}

// Opt-in only: normal tests never contact the developer's database.
// This performs a connection check, not schema changes or migrations.
func TestConnectPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to verify a real PostgreSQL connection")
	}
	pool, err := Connect(url)
	if err != nil {
		t.Fatal("PostgreSQL connection failed; connection details omitted to protect credentials")
	}
	if pool.Stats().MaxOpenConnections != 10 {
		t.Error("unexpected pool connection limit")
	}
	if err := pool.Close(); err != nil {
		t.Error("could not close PostgreSQL pool")
	}
}
