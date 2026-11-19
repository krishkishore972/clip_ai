// Package job owns Redis-backed background-task delivery using Asynq.
package job

import (
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/redis/go-redis/v9"
)

// NewClient follows Asynq's producer setup. The caller owns the client and must
// Close it at process shutdown. Construction is lazy: use Ping to verify Redis
// availability, and EnqueueContext with a request/job deadline to submit work.
// No tasks are submitted automatically, and no worker is started here.
func NewClient(cfg config.Redis) *asynq.Client {
	return asynq.NewClient(contextRedisOptions{cfg.Options})
}

// Asynq's built-in RedisClientOpt does not enable go-redis command deadlines.
// Keep Asynq's client ownership/Close semantics while configuring that existing
// transport to honor EnqueueContext. The outbox owns retries, not go-redis.
type contextRedisOptions struct{ asynq.RedisClientOpt }

func (opt contextRedisOptions) MakeRedisClient() interface{} {
	return redis.NewClient(&redis.Options{
		Network:               opt.Network,
		Addr:                  opt.Addr,
		Username:              opt.Username,
		Password:              opt.Password,
		DB:                    opt.DB,
		DialTimeout:           opt.DialTimeout,
		ReadTimeout:           opt.ReadTimeout,
		WriteTimeout:          opt.WriteTimeout,
		PoolSize:              opt.PoolSize,
		TLSConfig:             opt.TLSConfig,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
	})
}
