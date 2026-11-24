package curationworker_test

import (
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/app/curationworker"
)

func TestDisabledCurationWorkerDoesNotNeedServices(t *testing.T) {
	t.Setenv("CURATION_WORKER_ENABLED", "false")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")
	if err := curationworker.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestCurationWorkerFailsBeforeConnections(t *testing.T) {
	t.Setenv("CURATION_WORKER_ENABLED", "true")
	t.Setenv("CURATION_SERVICE_TOKEN", "secret")
	if err := curationworker.Run(); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid config accepted/exposed")
	}
}
