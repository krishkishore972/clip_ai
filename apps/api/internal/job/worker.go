package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
)

// ValidationWorkerConfig consumes only validation tasks. The durable handler
// owns the three-retry budget, including tasks enqueued with legacy options.
func ValidationWorkerConfig(logger *slog.Logger) asynq.Config {
	if logger == nil {
		logger = slog.Default()
	}
	return asynq.Config{
		Concurrency: 1,
		Queues:      map[string]int{ValidationQueue: 1},
		// Shutdown interrupts unfinished work after this drain budget and Asynq
		// requeues it. Handler cancellation must clean up before process exit;
		// durable leases/recovery cover interrupted cleanup and lost owners.
		ShutdownTimeout: 30 * time.Second,
		RetryDelayFunc:  validationRetryDelay,
		Logger:          validationQueueLogger{logger: logger},
		LogLevel:        asynq.DebugLevel,
	}
}

// NewValidationInspector bounds individual background-context inspection
// commands; Asynq's inspector does not accept caller contexts. Disable transport
// retries and use short socket budgets so recovery can observe cancellation.
func NewValidationInspector(cfg config.Redis) *asynq.Inspector {
	options := cfg.Options
	options.DialTimeout = time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	return asynq.NewInspector(contextRedisOptions{options})
}

func validationRetryDelay(n int, _ error, _ *asynq.Task) time.Duration {
	if n <= 0 {
		return time.Minute
	}
	if n == 1 {
		return 2 * time.Minute
	}
	return 4 * time.Minute
}

// Asynq's messages can embed Redis credentials, payloads, and raw provider
// errors. Do not format, inspect, or forward any supplied arguments.
type validationQueueLogger struct{ logger *slog.Logger }

func (l validationQueueLogger) log(level slog.Level, code string) {
	l.logger.Log(context.Background(), level, "validation queue event", "code", code)
}
func (l validationQueueLogger) Debug(...interface{}) { l.log(slog.LevelDebug, "QUEUE_DEBUG") }
func (l validationQueueLogger) Info(...interface{})  { l.log(slog.LevelInfo, "QUEUE_INFO") }
func (l validationQueueLogger) Warn(...interface{})  { l.log(slog.LevelWarn, "QUEUE_WARN") }
func (l validationQueueLogger) Error(...interface{}) { l.log(slog.LevelError, "QUEUE_ERROR") }
func (l validationQueueLogger) Fatal(...interface{}) { l.log(slog.LevelError, "QUEUE_FATAL") }
