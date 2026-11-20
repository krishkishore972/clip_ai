package app_test

import (
	"context"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/app/mediaworker"
)

func TestMediaWorkerDisabled(t *testing.T) {
	t.Setenv("MEDIA_PREPARATION_WORKER_ENABLED", "false")
	if err := mediaworker.Run(); err != nil {
		t.Fatal(err)
	}
}
func TestMediaWorkerSchemaFailsClosed(t *testing.T) {
	if err := mediaworker.CheckSchema(context.Background(), nil); err == nil {
		t.Fatal("nil schema accepted")
	}
}
