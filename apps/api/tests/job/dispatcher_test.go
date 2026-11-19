package job_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/job"
)

type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	return nil, nil
}

func TestDispatcherOptions(t *testing.T) {
	t.Parallel()
	valid := job.DispatcherOptions{
		PollInterval:       time.Second,
		TransactionTimeout: 10 * time.Second,
		EnqueueTimeout:     time.Second,
		RetryBase:          time.Second,
		RetryMax:           time.Minute,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*job.DispatcherOptions){
		func(o *job.DispatcherOptions) { o.PollInterval = 0 },
		func(o *job.DispatcherOptions) { o.EnqueueTimeout = time.Nanosecond },
		func(o *job.DispatcherOptions) { o.TransactionTimeout = o.EnqueueTimeout },
		func(o *job.DispatcherOptions) { o.RetryBase = 0 },
		func(o *job.DispatcherOptions) { o.RetryMax = o.RetryBase / 2 },
	} {
		options := valid
		mutate(&options)
		if _, err := job.NewDispatcher(&sql.DB{}, noopEnqueuer{}, options); err == nil {
			t.Fatal("invalid dispatcher options accepted")
		}
	}
	if _, err := job.NewDispatcher(nil, noopEnqueuer{}, valid); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := job.NewDispatcher(&sql.DB{}, nil, valid); err == nil {
		t.Fatal("missing client accepted")
	}
}

func TestRetryDelay(t *testing.T) {
	t.Parallel()
	base, ceiling := time.Second, time.Minute
	for attempt, capDelay := range map[int]time.Duration{
		1: base, 2: 2 * base, 3: 4 * base, 7: ceiling, 2_147_483_647: ceiling,
	} {
		for range 100 {
			delay := job.DispatchRetryDelay(attempt, base, ceiling)
			if delay < capDelay/2 || delay > capDelay {
				t.Fatalf("attempt %d delay %s outside [%s, %s]", attempt, delay, capDelay/2, capDelay)
			}
		}
	}
}
