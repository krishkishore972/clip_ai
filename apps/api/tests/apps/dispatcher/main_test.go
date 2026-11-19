package dispatcher_test

import (
	"testing"

	"github.com/krishkishore972/clip_ai/internal/app/dispatcher"
)

func TestDisabledRunDoesNotConnect(t *testing.T) {
	t.Setenv("OUTBOX_DISPATCH_ENABLED", "false")
	t.Setenv("DATABASE_URL", "invalid")
	t.Setenv("REDIS_URL", "invalid")
	if err := dispatcher.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsMissingConfiguration(t *testing.T) {
	t.Setenv("OUTBOX_DISPATCH_ENABLED", "")
	if err := dispatcher.Run(); err == nil {
		t.Fatal("expected missing configuration to fail without starting")
	}
}
