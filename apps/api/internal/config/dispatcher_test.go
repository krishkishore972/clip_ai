package config

import (
	"strings"
	"testing"
)

func dispatcherEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"OUTBOX_DISPATCH_ENABLED":    "true",
		"DATABASE_URL":               "postgres://fake.invalid/db",
		"API_LOG_LEVEL":              "info",
		"OUTBOX_POLL_INTERVAL":       "1s",
		"OUTBOX_TRANSACTION_TIMEOUT": "10s",
		"OUTBOX_ENQUEUE_TIMEOUT":     "3s",
		"OUTBOX_RETRY_BASE":          "1s",
		"OUTBOX_RETRY_MAX":           "5m",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadDispatcher(t *testing.T) {
	dispatcherEnv(t)
	cfg, err := LoadDispatcher()
	if err != nil || !cfg.Enabled {
		t.Fatalf("valid dispatcher config: %v", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"OUTBOX_DISPATCH_ENABLED", ""},
		{"OUTBOX_DISPATCH_ENABLED", "invalid"},
		{"DATABASE_URL", ""},
		{"API_LOG_LEVEL", "invalid"},
		{"OUTBOX_POLL_INTERVAL", ""},
		{"OUTBOX_POLL_INTERVAL", "1ns"},
		{"OUTBOX_ENQUEUE_TIMEOUT", "invalid"},
		{"OUTBOX_ENQUEUE_TIMEOUT", "0s"},
		{"OUTBOX_TRANSACTION_TIMEOUT", "2s"},
		{"OUTBOX_RETRY_BASE", "0s"},
		{"OUTBOX_RETRY_MAX", "500ms"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadDispatcher(); err == nil {
				t.Fatal("invalid dispatcher configuration accepted")
			}
		})
	}
}

func TestDisabledDispatcherNeedsNoDependencies(t *testing.T) {
	t.Setenv("OUTBOX_DISPATCH_ENABLED", "false")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")
	if cfg, err := LoadDispatcher(); err != nil || cfg.Enabled {
		t.Fatalf("disabled dispatcher should not require DB/Redis: %v", err)
	}
}

func TestDispatcherDoesNotEchoDatabaseURL(t *testing.T) {
	dispatcherEnv(t)
	t.Setenv("DATABASE_URL", "postgres://fake-secret.invalid/db")
	t.Setenv("OUTBOX_RETRY_BASE", "")
	_, err := LoadDispatcher()
	if err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("configuration error must not echo database credentials")
	}
}
