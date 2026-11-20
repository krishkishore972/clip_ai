package job

import (
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
)

// ProcessWorkerConfig is the only media-consumer policy. It accepts the
// initial validation event and later process/preparation event types in one
// bounded worker.
func ProcessWorkerConfig(logger *slog.Logger) asynq.Config {
	if logger == nil {
		logger = slog.Default()
	}
	return asynq.Config{
		Concurrency:     1,
		Queues:          map[string]int{ProcessQueue: 1, ValidationQueue: 1},
		ShutdownTimeout: 30 * time.Second,
		RetryDelayFunc:  func(_ int, _ error, _ *asynq.Task) time.Duration { return time.Minute },
		Logger:          processQueueLogger{logger: logger},
		LogLevel:        asynq.DebugLevel,
	}
}

// Asynq messages can contain credentials, payloads, and provider errors. Do
// not format, inspect, or forward any supplied arguments.
type processQueueLogger struct{ logger *slog.Logger }

func (l processQueueLogger) Debug(...interface{}) {
	l.logger.Debug("media process queue event", "code", "QUEUE_DEBUG")
}
func (l processQueueLogger) Info(...interface{}) {
	l.logger.Info("media process queue event", "code", "QUEUE_INFO")
}
func (l processQueueLogger) Warn(...interface{}) {
	l.logger.Warn("media process queue event", "code", "QUEUE_WARN")
}
func (l processQueueLogger) Error(...interface{}) {
	l.logger.Error("media process queue event", "code", "QUEUE_ERROR")
}
func (l processQueueLogger) Fatal(...interface{}) {
	l.logger.Error("media process queue event", "code", "QUEUE_FATAL")
}
