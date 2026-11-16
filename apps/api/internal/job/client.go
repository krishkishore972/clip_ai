// Package job owns Redis-backed background-task delivery using Asynq.
package job

import (
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
)

// NewClient follows Asynq's producer setup. The caller owns the client and must
// Close it at process shutdown. Construction is lazy: use Ping to verify Redis
// availability, and EnqueueContext with a request/job deadline to submit work.
// No tasks are submitted automatically, and no worker is started here.
func NewClient(cfg config.Redis) *asynq.Client {
	return asynq.NewClient(cfg.Options)
}
