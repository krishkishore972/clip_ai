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

// ValidationWorker contains process configuration, not tunable validation policy.
// DatabaseURL is secret and must never be logged.
type ValidationWorker struct {
	PreparationEnabled bool
	Enabled            bool
	DatabaseURL        string
	LogLevel           slog.Level
	Region             string
	Bucket             string
	TempDir            string
}

func LoadValidationWorker() (ValidationWorker, error) {
	var cfg ValidationWorker
	var featureErr error
	cfg.PreparationEnabled, featureErr = LoadPreparationEnabled()
	if featureErr != nil {
		return ValidationWorker{}, featureErr
	}
	raw, err := required("VALIDATION_WORKER_ENABLED")
	if err != nil {
		return cfg, err
	}
	cfg.Enabled, err = strconv.ParseBool(raw)
	if err != nil {
		return ValidationWorker{}, errors.New("config: VALIDATION_WORKER_ENABLED must be a boolean")
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	var errs []error
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"DATABASE_URL", &cfg.DatabaseURL},
		{"AWS_REGION", &cfg.Region},
		{"S3_BUCKET", &cfg.Bucket},
		{"VALIDATION_TEMP_DIR", &cfg.TempDir},
	} {
		if *field.dst, err = required(field.key); err != nil {
			errs = append(errs, err)
		}
	}
	level, err := required("API_LOG_LEVEL")
	if err == nil {
		cfg.LogLevel, err = parseLevel(level)
	}
	if err != nil {
		errs = append(errs, errors.New("config: API_LOG_LEVEL must be debug, info, warn, or error"))
	}
	// Check the untrimmed bucket too: silently normalizing a resource name can
	// connect a misconfigured worker to a different resource than intended.
	if cfg.Bucket != "" && (strings.ContainsAny(cfg.Bucket, "/\\") || strings.IndexFunc(os.Getenv("S3_BUCKET"), unicode.IsSpace) >= 0) {
		errs = append(errs, errors.New("config: S3_BUCKET must not contain slashes or whitespace"))
	}
	if cfg.TempDir != "" && !filepath.IsAbs(cfg.TempDir) {
		errs = append(errs, errors.New("config: VALIDATION_TEMP_DIR must be an absolute path"))
	}
	if len(errs) != 0 {
		return ValidationWorker{}, errors.Join(errs...)
	}
	return cfg, nil
}
