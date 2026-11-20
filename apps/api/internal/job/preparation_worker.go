package job

import (
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

// PreparationWorkerConfig isolates expensive tasks and keeps validation unchanged.
func PreparationWorkerConfig(logger *slog.Logger) asynq.Config {
	cfg := ValidationWorkerConfig(logger)
	cfg.Queues = map[string]int{preparation.Queue: 1}
	cfg.RetryDelayFunc = func(_ int, _ error, _ *asynq.Task) time.Duration { return time.Minute }
	return cfg
}
