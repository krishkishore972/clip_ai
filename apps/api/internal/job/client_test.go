package job

import (
	"os"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
)

func TestNewClientIsLazyAndCanClose(t *testing.T) {
	t.Parallel()
	// An unavailable address is deliberate: construction must not dial Redis.
	client := NewClient(config.Redis{Options: asynq.RedisClientOpt{Addr: "127.0.0.1:1"}})
	if err := client.Close(); err != nil {
		t.Fatal("close client failed")
	}
}

func TestClientPingRedis(t *testing.T) {
	// Opt-in, connection-only check: never enqueue tasks or flush/delete keys.
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("set TEST_REDIS_URL to test a Redis connection")
	}
	t.Setenv("REDIS_URL", raw)
	cfg, err := config.LoadRedis()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(cfg)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error("close client failed")
		}
	})
	if err := client.Ping(); err != nil {
		// Redis/network errors are intentionally not printed with connection data.
		t.Fatal("Redis ping failed; check TEST_REDIS_URL and Redis availability")
	}
}
