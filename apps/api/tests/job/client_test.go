package job_test

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/job"
)

func TestProducerHonorsCommandContexts(t *testing.T) {
	transport := job.NewProducerRedisClient(asynq.RedisClientOpt{Addr: "127.0.0.1:1"})
	defer transport.Close()
	if !transport.Options().ContextTimeoutEnabled || transport.Options().MaxRetries != 0 {
		t.Fatal("producer must honor command contexts with automatic retries disabled")
	}
}

func TestEnqueueDeadlineAgainstUnresponsiveRedis(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connections sync.WaitGroup
	stop := make(chan struct{})
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer connection.Close()
				<-stop
			}()
		}
	}()
	defer func() {
		listener.Close()
		<-acceptDone
		close(stop)
		connections.Wait()
	}()
	client := job.NewClient(config.Redis{Options: asynq.RedisClientOpt{
		Addr:         listener.Addr().String(),
		DialTimeout:  5 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client.EnqueueContext(ctx, asynq.NewTask("test:deadline", nil))
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("enqueue did not respect its short context budget")
	}
}

func TestNewClientIsLazyAndCanClose(t *testing.T) {
	t.Parallel()
	// An unavailable address is deliberate: construction must not dial Redis.
	client := job.NewClient(config.Redis{Options: asynq.RedisClientOpt{Addr: "127.0.0.1:1"}})
	if err := client.Close(); err != nil {
		t.Fatal("close client failed")
	}
}

func TestClientPingRedis(t *testing.T) {
	// Opt-in, connection-only check: never enqueue tasks or flush/delete keys.
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("set TEST_REDIS_URL to test a Redis connection")
	}
	t.Setenv("REDIS_URL", raw)
	cfg, err := config.LoadRedis()
	if err != nil {
		t.Fatal(err)
	}
	client := job.NewClient(cfg)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error("close client failed")
		}
	})
	if err := client.Ping(); err != nil {
		// Redis/network errors are intentionally not printed with connection data.
		t.Fatal("Redis ping failed; check TEST_REDIS_URL and Redis availability")
	}
}
