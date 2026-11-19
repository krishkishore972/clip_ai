package main

import (
	"strings"
	"testing"
)

func TestDisabledWorkerStartsWithoutDependencies(t *testing.T) {
	t.Setenv("VALIDATION_WORKER_ENABLED", "false")
	for _, key := range []string{"DATABASE_URL", "REDIS_URL", "AWS_REGION", "S3_BUCKET", "VALIDATION_TEMP_DIR", "API_LOG_LEVEL", "PATH"} {
		t.Setenv(key, "")
	}
	if err := run(); err != nil {
		t.Fatalf("disabled worker touched dependencies: %v", err)
	}
}

func TestWorkerRejectsInvalidConfigurationBeforeDependencies(t *testing.T) {
	for _, tc := range []struct{ name, enabled string }{
		{"missing opt-in", ""},
		{"invalid opt-in", "fake-secret"},
		{"enabled missing configuration", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VALIDATION_WORKER_ENABLED", tc.enabled)
			t.Setenv("DATABASE_URL", "postgres://fake-secret.invalid/db")
			for _, key := range []string{"AWS_REGION", "S3_BUCKET", "VALIDATION_TEMP_DIR", "API_LOG_LEVEL"} {
				t.Setenv(key, "")
			}
			err := run()
			if err == nil || strings.Contains(err.Error(), "fake-secret") {
				t.Fatal("invalid configuration must fail without echoing credentials")
			}
		})
	}
}

func TestWorkerRejectsInvalidRedisBeforeConnecting(t *testing.T) {
	for key, value := range map[string]string{
		"VALIDATION_WORKER_ENABLED": "true",
		"DATABASE_URL":              "postgres://fake-secret.invalid/db",
		"API_LOG_LEVEL":             "info",
		"AWS_REGION":                "us-east-1",
		"S3_BUCKET":                 "private-test-bucket",
		"VALIDATION_TEMP_DIR":       "/tmp/validation-test",
		"REDIS_URL":                 "redis://fake-secret.invalid",
	} {
		t.Setenv(key, value)
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "REDIS_URL") || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("invalid Redis must fail offline with a sanitized error")
	}
}
