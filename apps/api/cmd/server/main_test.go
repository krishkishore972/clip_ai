package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

func TestServeRejectsDatabaseFailureWithoutLeakingDSN(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := serve(config.Config{DatabaseURL: "postgres://secret-user:secret-password@localhost:invalid-port/database"}, logger)
	if err == nil {
		t.Fatal("startup accepted an invalid database connection")
	}
	if !strings.Contains(err.Error(), "database connection failed") || strings.Contains(err.Error(), "secret-") {
		t.Fatal("startup error must explain connection failure without exposing the DSN")
	}
}

func TestUploadRoutesDevelopmentOnly(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"development", "test", "production", "staging", ""} {
		t.Run(env, func(t *testing.T) {
			t.Parallel()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			h := newHandler(logger, env, nil)
			r := httptest.NewRequest(http.MethodPost, "/uploads", strings.NewReader(
				`{"filename":"sample.mp4","sizeBytes":1024,"contentType":"video/mp4"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusNotFound
			if env == "development" {
				want = http.StatusUnauthorized
				if !strings.Contains(w.Body.String(), `"code":"UNAUTHENTICATED"`) {
					t.Fatalf("unexpected body: %s", w.Body.String())
				}
			}
			if w.Code != want {
				t.Fatalf("status=%d, want %d", w.Code, want)
			}
			if w.Header().Get("X-Request-Id") == "" {
				t.Fatal("missing middleware request ID")
			}
		})
	}
}

func TestDevelopmentUploadsStartup(t *testing.T) {
	t.Setenv("DEV_S3_UPLOAD_ENABLED", "false")
	if service, err := developmentUploads(context.Background(), "development",upload.SQLAssets{}); err != nil || service != nil {
		t.Fatal("disabled startup attempted upload initialization")
	}
	t.Setenv("DEV_S3_UPLOAD_ENABLED", "true")
	if _, err := developmentUploads(context.Background(), "production",upload.SQLAssets{}); err == nil {
		t.Fatal("production enabled unguarded uploads")
	}
	t.Setenv("AWS_REGION", "")
	if _, err := developmentUploads(context.Background(), "development",upload.SQLAssets{}); err == nil {
		t.Fatal("enabled startup accepted missing configuration")
	}
	// Construct the real adapter with isolated fake credentials. No upload request
	// is sent: this verifies composition, not connectivity to AWS.
	for key, value := range map[string]string{
		"AWS_REGION": "ap-south-1", "S3_BUCKET": "private-example-bucket", "S3_PRESIGN_TTL": "15m",
		"UPLOAD_TOKEN_SECRET": strings.Repeat("test-only-", 8), "UPLOAD_TOKEN_TTL": "24h",
		"AWS_ACCESS_KEY_ID": "TESTKEY", "AWS_SECRET_ACCESS_KEY": "not-real", "AWS_SESSION_TOKEN": "",
		"AWS_PROFILE": "", "AWS_DEFAULT_PROFILE": "", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_CONFIG_FILE":             filepath.Join(t.TempDir(), "missing-config"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(t.TempDir(), "missing-credentials"),
	} {
		t.Setenv(key, value)
	}
	if service, err := developmentUploads(context.Background(), "development",upload.SQLAssets{}); err != nil || service == nil {
		t.Fatalf("SDK/service composition failed: %v", err)
	}
}

func TestHealthRouteUnchanged(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := httptest.NewRecorder()
	newHandler(logger, "production", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}
