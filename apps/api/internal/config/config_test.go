package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

var envKeys = []string{
	"DATABASE_URL",
	"API_ADDR",
	"API_ENV",
	"API_LOG_LEVEL",
	"API_READ_HEADER_TIMEOUT",
	"API_READ_TIMEOUT",
	"API_WRITE_TIMEOUT",
	"API_IDLE_TIMEOUT",
	"API_SHUTDOWN_TIMEOUT",
}

// clearEnv neutralises every variable Load reads so tests do not depend on
// the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
}

// setValidEnv populates every required variable, allowing a test to override
// exactly one of them.
func setValidEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("API_ADDR", ":8080")
	t.Setenv("API_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://example:fake@localhost:5432/clip_ai")
	t.Setenv("API_LOG_LEVEL", "info")
	t.Setenv("API_READ_HEADER_TIMEOUT", "5s")
	t.Setenv("API_READ_TIMEOUT", "30s")
	t.Setenv("API_WRITE_TIMEOUT", "30s")
	t.Setenv("API_IDLE_TIMEOUT", "120s")
	t.Setenv("API_SHUTDOWN_TIMEOUT", "15s")
}

func TestLoad(t *testing.T) {
	setValidEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.DatabaseURL != "postgres://example:fake@localhost:5432/clip_ai" {
		t.Error("DatabaseURL does not match the configured value")
	}
	if cfg.Env != "development" {
		t.Errorf("Env = %q, want development", cfg.Env)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", cfg.ReadHeaderTimeout)
	}
	if cfg.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want 30s", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 2m", cfg.IdleTimeout)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
}

func TestLoadAcceptsOverrides(t *testing.T) {
	setValidEnv(t)
	t.Setenv("API_ADDR", ":9090")
	t.Setenv("API_ENV", "production")
	t.Setenv("API_LOG_LEVEL", "debug")
	t.Setenv("API_READ_TIMEOUT", "45s")
	t.Setenv("API_SHUTDOWN_TIMEOUT", "1m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Addr != ":9090" {
		t.Errorf("Addr = %q, want :9090", cfg.Addr)
	}
	if cfg.Env != "production" {
		t.Errorf("Env = %q, want production", cfg.Env)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if cfg.ReadTimeout != 45*time.Second {
		t.Errorf("ReadTimeout = %v, want 45s", cfg.ReadTimeout)
	}
	if cfg.ShutdownTimeout != time.Minute {
		t.Errorf("ShutdownTimeout = %v, want 1m", cfg.ShutdownTimeout)
	}
}

func TestLoadRequiresEveryVariable(t *testing.T) {
	clearEnv(t)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error when nothing is set")
	}

	for _, key := range envKeys {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention missing %s:\n%v", key, err)
		}
	}
}

func TestLoadReportsOnlyMissingVariable(t *testing.T) {
	setValidEnv(t)
	t.Setenv("API_READ_TIMEOUT", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error for unset API_READ_TIMEOUT")
	}
	if !strings.Contains(err.Error(), "API_READ_TIMEOUT is required") {
		t.Errorf("error %q does not report the missing key", err)
	}
	if strings.Contains(err.Error(), "API_ADDR is required") {
		t.Errorf("error %q reports a variable that is set", err)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "unparseable duration", key: "API_READ_TIMEOUT", val: "thirty"},
		{name: "zero duration", key: "API_WRITE_TIMEOUT", val: "0s"},
		{name: "negative duration", key: "API_IDLE_TIMEOUT", val: "-1s"},
		{name: "unknown log level", key: "API_LOG_LEVEL", val: "loud"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv(tt.key, tt.val)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error for %s=%q", tt.key, tt.val)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not mention %s", err, tt.key)
			}
		})
	}
}
