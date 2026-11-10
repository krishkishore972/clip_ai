package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

var envKeys = []string{
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

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
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

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
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
			clearEnv(t)
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
