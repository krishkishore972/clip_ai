package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/config"
)

func TestPreparationWorkerOptIn(t *testing.T) {
	t.Setenv("MEDIA_PREPARATION_WORKER_ENABLED", "false")
	t.Setenv("MEDIA_PREPARATION_ENABLED", "false")
	cfg, err := config.LoadPreparationWorker()
	if err != nil || cfg.Enabled {
		t.Fatalf("disabled: %+v %v", cfg, err)
	}
	t.Setenv("MEDIA_PREPARATION_WORKER_ENABLED", "invalid secret")
	if _, err := config.LoadPreparationWorker(); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid boolean not safely rejected")
	}
	t.Setenv("MEDIA_PREPARATION_WORKER_ENABLED", "true")
	if _, err := config.LoadPreparationWorker(); err == nil {
		t.Fatal("worker enabled without feature")
	}
	t.Setenv("MEDIA_PREPARATION_ENABLED", "true")
	for key, value := range map[string]string{"DATABASE_URL": "postgres://fake:fake@localhost/test", "AWS_REGION": "test-region", "S3_BUCKET": "private-test", "API_LOG_LEVEL": "info", "MEDIA_PREPARATION_TEMP_DIR": filepath.Join(t.TempDir(), "preparation"), "VALIDATION_TEMP_DIR": filepath.Join(t.TempDir(), "validation")} {
		t.Setenv(key, value)
	}
	cfg, err = config.LoadPreparationWorker()
	if err != nil || !cfg.Enabled {
		t.Fatalf("enabled: %+v %v", cfg, err)
	}
	t.Setenv("VALIDATION_TEMP_DIR", cfg.TempDir)
	if _, err := config.LoadPreparationWorker(); err == nil {
		t.Fatal("shared temporary root accepted")
	}
}
