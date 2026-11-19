package job_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/job"
)

func TestValidationWorkerPolicy(t *testing.T) {
	cfg := job.ValidationWorkerConfig(nil)
	if cfg.Concurrency != 1 || cfg.ShutdownTimeout != 30*time.Second || len(cfg.Queues) != 1 || cfg.Queues[job.ValidationQueue] != 1 {
		t.Fatal("worker must consume validation only with one worker and a bounded drain")
	}
	if cfg.ErrorHandler != nil || cfg.IsFailure != nil || cfg.BaseContext != nil {
		t.Fatal("handler owns persistence; Asynq must not inherit signal cancellation")
	}
	for _, tc := range []struct {
		retry int
		delay time.Duration
	}{
		{-1, time.Minute}, {0, time.Minute}, {1, 2 * time.Minute}, {2, 4 * time.Minute}, {25, 4 * time.Minute},
	} {
		if got := cfg.RetryDelayFunc(tc.retry, errors.New("ignored"), asynq.NewTask(job.ValidationTaskType, nil)); got != tc.delay {
			t.Fatalf("retry %d delay = %v, want %v", tc.retry, got, tc.delay)
		}
	}
}

type forbiddenQueueLogValue struct{}

func (forbiddenQueueLogValue) String() string { panic("raw arguments must not be formatted") }

func TestValidationQueueLoggerDropsRawArguments(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := job.ValidationWorkerConfig(logger)
	for _, log := range []func(...interface{}){cfg.Logger.Debug, cfg.Logger.Info, cfg.Logger.Warn, cfg.Logger.Error, cfg.Logger.Fatal} {
		log("redis://user:fake-secret@host", errors.New("private payload"), forbiddenQueueLogValue{})
	}
	text := output.String()
	if strings.Contains(text, "fake-secret") || strings.Contains(text, "private payload") || strings.Count(text, "validation queue event") != 5 {
		t.Fatal("queue logger must emit only fixed event codes and levels")
	}
	for _, code := range []string{"QUEUE_DEBUG", "QUEUE_INFO", "QUEUE_WARN", "QUEUE_ERROR", "QUEUE_FATAL"} {
		if !strings.Contains(text, code) {
			t.Fatalf("missing safe log code %s", code)
		}
	}
}
