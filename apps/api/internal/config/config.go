// Package config loads process configuration from environment variables.
//
// There are no defaults in code: every variable is required. The canonical
// development values live in apps/api/.env.example (copied to .env, which the
// Makefile exports to every target); deployed environments inject the same
// keys directly. Load fails fast and reports every missing or malformed
// variable in one error, so a misconfigured deployment is fixed in one pass.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config is the resolved, validated configuration for the API process.
type Config struct {
	Addr              string        // API_ADDR: host:port to listen on
	Env               string        // API_ENV: "development", "test", "production"
	LogLevel          slog.Level    // API_LOG_LEVEL: minimum level emitted to stdout
	ReadHeaderTimeout time.Duration // API_READ_HEADER_TIMEOUT: slowloris protection
	ReadTimeout       time.Duration // API_READ_TIMEOUT: full request read budget
	WriteTimeout      time.Duration // API_WRITE_TIMEOUT: response write budget
	IdleTimeout       time.Duration // API_IDLE_TIMEOUT: keep-alive idle budget
	ShutdownTimeout   time.Duration // API_SHUTDOWN_TIMEOUT: drain budget on SIGTERM
}

// Load reads the environment into a Config and validates it.
func Load() (Config, error) {
	var (
		cfg  Config
		errs []error
	)

	addr, err := required("API_ADDR")
	if err != nil {
		errs = append(errs, err)
	}
	cfg.Addr = addr

	env, err := required("API_ENV")
	if err != nil {
		errs = append(errs, err)
	}
	cfg.Env = env

	if cfg.LogLevel, err = requiredLevel("API_LOG_LEVEL"); err != nil {
		errs = append(errs, err)
	}

	for _, f := range []struct {
		key string
		dst *time.Duration
	}{
		{key: "API_READ_HEADER_TIMEOUT", dst: &cfg.ReadHeaderTimeout},
		{key: "API_READ_TIMEOUT", dst: &cfg.ReadTimeout},
		{key: "API_WRITE_TIMEOUT", dst: &cfg.WriteTimeout},
		{key: "API_IDLE_TIMEOUT", dst: &cfg.IdleTimeout},
		{key: "API_SHUTDOWN_TIMEOUT", dst: &cfg.ShutdownTimeout},
	} {
		d, err := requiredDuration(f.key)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*f.dst = d
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}

	return cfg, nil
}

// required returns the trimmed value of key, or an error when unset or empty.
func required(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("config: %s is required", key)
	}
	return v, nil
}

// requiredDuration parses key with time.ParseDuration (e.g. "30s", "2m").
func requiredDuration(key string) (time.Duration, error) {
	raw, err := required(key)
	if err != nil {
		return 0, err
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

// requiredLevel reads a level-name variable and maps it to slog.Level.
func requiredLevel(key string) (slog.Level, error) {
	raw, err := required(key)
	if err != nil {
		return 0, err
	}

	level, err := parseLevel(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return level, nil
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
