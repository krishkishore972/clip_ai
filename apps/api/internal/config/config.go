// Package config loads process configuration from environment variables.
//
// Load is called once at startup. It fails fast on malformed values, so a bad
// deployment is visible immediately instead of silently falling back to a
// default.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Defaults, each overridable by the environment variable named in the comment.
const (
	defaultAddr              = ":8080"           // API_ADDR
	defaultEnv               = "development"     // API_ENV
	defaultLogLevel          = "info"            // API_LOG_LEVEL
	defaultReadHeaderTimeout = 5 * time.Second   // API_READ_HEADER_TIMEOUT
	defaultReadTimeout       = 30 * time.Second  // API_READ_TIMEOUT
	defaultWriteTimeout      = 30 * time.Second  // API_WRITE_TIMEOUT
	defaultIdleTimeout       = 120 * time.Second // API_IDLE_TIMEOUT
	defaultShutdownTimeout   = 15 * time.Second  // API_SHUTDOWN_TIMEOUT
)

// Config is the resolved, validated configuration for the API process.
type Config struct {
	Addr              string        // host:port to listen on
	Env               string        // "development", "test", "production"
	LogLevel          slog.Level    // minimum level emitted to stdout
	ReadHeaderTimeout time.Duration // slowloris protection
	ReadTimeout       time.Duration // full request read budget
	WriteTimeout      time.Duration // response write budget
	IdleTimeout       time.Duration // keep-alive idle budget
	ShutdownTimeout   time.Duration // drain budget on SIGTERM
}

// Load reads the environment into a Config and validates it.
func Load() (Config, error) {
	cfg := Config{
		Addr: getString("API_ADDR", defaultAddr),
		Env:  getString("API_ENV", defaultEnv),
	}

	level, err := parseLevel(getString("API_LOG_LEVEL", defaultLogLevel))
	if err != nil {
		return Config{}, fmt.Errorf("config: API_LOG_LEVEL: %w", err)
	}
	cfg.LogLevel = level

	cfg.ReadHeaderTimeout, err = getDuration("API_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	if err != nil {
		return Config{}, err
	}

	cfg.ReadTimeout, err = getDuration("API_READ_TIMEOUT", defaultReadTimeout)
	if err != nil {
		return Config{}, err
	}

	cfg.WriteTimeout, err = getDuration("API_WRITE_TIMEOUT", defaultWriteTimeout)
	if err != nil {
		return Config{}, err
	}

	cfg.IdleTimeout, err = getDuration("API_IDLE_TIMEOUT", defaultIdleTimeout)
	if err != nil {
		return Config{}, err
	}

	cfg.ShutdownTimeout, err = getDuration("API_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// getString returns the value of key, or def when unset or empty.
func getString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// getDuration parses key with time.ParseDuration (e.g. "30s", "2m").
func getDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q: %w", key, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: %s=%q: must be positive", key, raw)
	}
	return d, nil
}

// parseLevel maps a level name to slog.Level, rejecting unknown names.
func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("must be one of debug, info, warn, error (got %q)", raw)
	}
}
