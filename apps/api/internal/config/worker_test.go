package config

import (
	"log/slog"
	"strings"
	"testing"
)

func validationWorkerEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"VALIDATION_WORKER_ENABLED": "true",
		"DATABASE_URL":              "postgres://fake.invalid/db",
		"API_LOG_LEVEL":             "debug",
		"AWS_REGION":                "us-east-1",
		"S3_BUCKET":                 "private-validation-test",
		"VALIDATION_TEMP_DIR":       "/tmp/validation-test",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadValidationWorker(t *testing.T) {
	validationWorkerEnv(t)
	cfg, err := LoadValidationWorker()
	if err != nil || !cfg.Enabled || cfg.LogLevel != slog.LevelDebug || cfg.Region != "us-east-1" || cfg.Bucket != "private-validation-test" || cfg.TempDir != "/tmp/validation-test" {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"VALIDATION_WORKER_ENABLED", ""},
		{"VALIDATION_WORKER_ENABLED", "invalid"},
		{"DATABASE_URL", ""},
		{"API_LOG_LEVEL", ""},
		{"API_LOG_LEVEL", "invalid"},
		{"AWS_REGION", ""},
		{"S3_BUCKET", ""},
		{"S3_BUCKET", "bucket/path"},
		{"S3_BUCKET", "bucket\\path"},
		{"S3_BUCKET", " bucket"},
		{"S3_BUCKET", "bucket "},
		{"S3_BUCKET", "bucket\tname"},
		{"VALIDATION_TEMP_DIR", ""},
		{"VALIDATION_TEMP_DIR", "relative/path"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadValidationWorker(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestDisabledValidationWorkerNeedsNoDependencies(t *testing.T) {
	t.Setenv("VALIDATION_WORKER_ENABLED", "false")
	for _, key := range []string{"DATABASE_URL", "REDIS_URL", "API_LOG_LEVEL", "AWS_REGION", "S3_BUCKET", "VALIDATION_TEMP_DIR"} {
		t.Setenv(key, "")
	}
	if cfg, err := LoadValidationWorker(); err != nil || cfg.Enabled {
		t.Fatalf("disabled worker requires no dependencies: %v", err)
	}
}

func TestValidationWorkerErrorsDoNotEchoValues(t *testing.T) {
	validationWorkerEnv(t)
	for _, key := range []string{"DATABASE_URL", "API_LOG_LEVEL", "S3_BUCKET", "VALIDATION_TEMP_DIR"} {
		t.Setenv(key, "fake-secret / invalid")
	}
	_, err := LoadValidationWorker()
	if err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("invalid configuration echoed sensitive values")
	}
}
