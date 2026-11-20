package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// PreparationWorker contains combined media-process configuration; policy budgets are fixed.
// DatabaseURL is secret and must never be logged.
type PreparationWorker struct {
	Enabled                 bool
	DatabaseURL             string
	LogLevel                slog.Level
	Region, Bucket, TempDir string
}

func LoadPreparationWorker() (PreparationWorker, error) {
	var cfg PreparationWorker
	raw, present := os.LookupEnv("MEDIA_PREPARATION_WORKER_ENABLED")
	if !present {
		return cfg, nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return cfg, errors.New("config: MEDIA_PREPARATION_WORKER_ENABLED must be a boolean")
	}
	if !enabled {
		return cfg, nil
	}
	feature, err := LoadPreparationEnabled()
	if err != nil {
		return cfg, err
	}
	if !feature {
		return cfg, errors.New("config: preparation worker requires MEDIA_PREPARATION_ENABLED")
	}
	cfg.Enabled = true
	var errs []error
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"DATABASE_URL", &cfg.DatabaseURL}, {"AWS_REGION", &cfg.Region}, {"S3_BUCKET", &cfg.Bucket}, {"MEDIA_PREPARATION_TEMP_DIR", &cfg.TempDir},
	} {
		if *field.dst, err = required(field.key); err != nil {
			errs = append(errs, err)
		}
	}
	if cfg.LogLevel, err = requiredLevel("API_LOG_LEVEL"); err != nil {
		errs = append(errs, errors.New("config: API_LOG_LEVEL must be debug, info, warn, or error"))
	}
	if cfg.Bucket != "" && (strings.ContainsAny(cfg.Bucket, "/\\") || strings.IndexFunc(os.Getenv("S3_BUCKET"), unicode.IsSpace) >= 0) {
		errs = append(errs, errors.New("config: S3_BUCKET must not contain slashes or whitespace"))
	}
	if cfg.TempDir != "" && (!filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) == string(filepath.Separator)) {
		errs = append(errs, errors.New("config: MEDIA_PREPARATION_TEMP_DIR must be a private absolute directory"))
	}
	// Never share a root with validation, including a configured parent/child.
	validation := strings.TrimSpace(os.Getenv("VALIDATION_TEMP_DIR"))
	if validation != "" && cfg.TempDir != "" {
		a, b := filepath.Clean(cfg.TempDir), filepath.Clean(validation)
		if a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) {
			errs = append(errs, errors.New("config: preparation and validation temporary roots must be separate"))
		}
	}
	if len(errs) > 0 {
		return PreparationWorker{}, errors.Join(errs...)
	}
	return cfg, nil
}
