package transcriptionworker_test

import (
	"testing"

	"github.com/krishkishore972/clip_ai/internal/app/transcriptionworker"
)

func TestWorkerDisabledWithoutDependencies(t *testing.T) {
	t.Setenv("TRANSCRIPTION_WORKER_ENABLED", "false")
	if err := transcriptionworker.Run(); err != nil {
		t.Fatal(err)
	}
}
