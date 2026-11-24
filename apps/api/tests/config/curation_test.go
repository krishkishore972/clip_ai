package config_test

import (
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/config"
)

func TestCurationWorkerConfig(t *testing.T) {
	t.Setenv("CURATION_WORKER_ENABLED", "false")
	if cfg, err := config.LoadCurationWorker(); err != nil || cfg.Enabled {
		t.Fatal("worker not disabled")
	}
	t.Setenv("CURATION_WORKER_ENABLED", "invalid")
	if _, err := config.LoadCurationWorker(); err == nil {
		t.Fatal("invalid flag accepted")
	}
	t.Setenv("CURATION_WORKER_ENABLED", "true")
	values := map[string]string{
		"DATABASE_URL": "postgres://offline", "AWS_REGION": "offline", "S3_BUCKET": "private",
		"CURATION_TEMP_DIR": "/var/tmp/offline-curation", "CURATION_SERVICE_URL": "http://127.0.0.1:8090/internal/curate",
		"CURATION_SERVICE_TOKEN": strings.Repeat("t", 32),
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	if cfg, err := config.LoadCurationWorker(); err != nil || !cfg.Enabled {
		t.Fatalf("valid config rejected: %v", err)
	}
	for key, value := range values {
		t.Setenv(key, "")
		if _, err := config.LoadCurationWorker(); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
		t.Setenv(key, value)
	}
	for key, value := range map[string]string{
		"CURATION_TEMP_DIR": "/", "S3_BUCKET": "bad/bucket",
		"CURATION_SERVICE_URL": "http://public.example/internal/curate", "CURATION_SERVICE_TOKEN": "secret",
	} {
		t.Setenv(key, value)
		if _, err := config.LoadCurationWorker(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe config accepted/exposed")
		}
		t.Setenv(key, values[key])
	}
}
