package config

import (
	"strings"
	"testing"
	"time"
)

func TestDevelopmentUploadDisabled(t *testing.T) {
	for _, flag := range []string{"", "false"} {
		t.Setenv("DEV_S3_UPLOAD_ENABLED", flag)
		t.Setenv("AWS_REGION", "")
		t.Setenv("S3_BUCKET", "")
		t.Setenv("UPLOAD_TOKEN_SECRET", "")
		cfg, err := LoadDevelopmentUpload("development")
		if err != nil || cfg.Enabled {
			t.Fatal("disabled upload configuration required AWS settings")
		}
	}
}

func setDevelopmentUploadEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DEV_S3_UPLOAD_ENABLED", "true")
	t.Setenv("AWS_REGION", "test-region")
	t.Setenv("S3_BUCKET", "private-example-bucket")
	t.Setenv("S3_PRESIGN_TTL", "15m")
	t.Setenv("UPLOAD_TOKEN_SECRET", strings.Repeat("test-only-", 8))
	t.Setenv("UPLOAD_TOKEN_TTL", "24h")
}

func TestDevelopmentUploadEnabled(t *testing.T) {
	setDevelopmentUploadEnv(t)
	cfg, err := LoadDevelopmentUpload("development")
	if err != nil || !cfg.Enabled || cfg.TokenTTL != 24*time.Hour || cfg.S3.Bucket != "private-example-bucket" || len(cfg.SigningSecret) < 32 {
		t.Fatalf("unexpected configuration error: %v", err)
	}
}

func TestDevelopmentUploadInvalidConfiguration(t *testing.T) {
	for _, tt := range []struct{ name, env, key, value string }{
		{"production", "production", "DEV_S3_UPLOAD_ENABLED", "true"},
		{"staging", "staging", "DEV_S3_UPLOAD_ENABLED", "true"},
		{"invalid flag", "development", "DEV_S3_UPLOAD_ENABLED", "yes-please"},
		{"missing secret", "development", "UPLOAD_TOKEN_SECRET", ""},
		{"weak secret", "development", "UPLOAD_TOKEN_SECRET", "short"},
		{"missing TTL", "development", "UPLOAD_TOKEN_TTL", ""},
		{"short TTL", "development", "UPLOAD_TOKEN_TTL", "30s"},
		{"long TTL", "development", "UPLOAD_TOKEN_TTL", "25h"},
		{"missing bucket", "development", "S3_BUCKET", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setDevelopmentUploadEnv(t)
			t.Setenv(tt.key, tt.value)
			if _, err := LoadDevelopmentUpload(tt.env); err == nil {
				t.Fatal("invalid enabled upload configuration accepted")
			}
		})
	}
}
