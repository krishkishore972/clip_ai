package migrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/job"
)

type enqueueFunc func(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)

func (f enqueueFunc) EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	return f(ctx, task, opts...)
}

// Uses only the disposable, isolation-checked schema from the parent test.
// Redis writes require a separate opt-in URL for an isolated test instance.
func testOutboxDispatcher(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	options := job.DispatcherOptions{
		PollInterval:       5 * time.Millisecond,
		TransactionTimeout: 2 * time.Second,
		EnqueueTimeout:     200 * time.Millisecond,
		RetryBase:          time.Second,
		RetryMax:           time.Minute,
	}
	newDispatcher := func(t *testing.T, client job.TaskEnqueuer) *job.Dispatcher {
		t.Helper()
		d, err := job.NewDispatcher(pool, client, options)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	reset := func(t *testing.T) {
		t.Helper()
		// No application data: these rows belong exclusively to this test schema.
		if _, err := pool.ExecContext(ctx, "DELETE FROM outbox_events"); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(t *testing.T, id, queue string) {
		t.Helper()
		const query = `
			INSERT INTO outbox_events (
				id,
				asset_id,
				task_type,
				queue_name,
				payload,
				dedupe_key
			)
			VALUES (
				$1,
				'owned',
				'media:validate',
				$2,
				'{"assetId":"owned"}',
				$1
			)
		`
		if _, err := pool.ExecContext(ctx, query, id, queue); err != nil {
			t.Fatal(err)
		}
	}
	taskID := func(opts []asynq.Option) string {
		for _, opt := range opts {
			if opt.Type() == asynq.TaskIDOpt {
				return opt.Value().(string)
			}
		}
		return ""
	}
	state := func(t *testing.T, id string) (bool, int, string, bool) {
		t.Helper()
		const query = `
			SELECT
				dispatched_at IS NOT NULL,
				dispatch_attempts,
				COALESCE(last_error_code, ''),
				next_attempt_at > clock_timestamp()
			FROM outbox_events
			WHERE id = $1
		`
		var delivered, future bool
		var attempts int
		var code string
		if err := pool.QueryRowContext(ctx, query, id).Scan(&delivered, &attempts, &code, &future); err != nil {
			t.Fatal(err)
		}
		return delivered, attempts, code, future
	}
	accept := enqueueFunc(func(_ context.Context, _ *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
		return nil, nil
	})

	t.Run("dispatch stored task and ignore future or delivered rows", func(t *testing.T) {
		reset(t)
		insert(t, "future", "validation")
		insert(t, "delivered", "validation")
		if _, err := pool.ExecContext(ctx, `
			UPDATE outbox_events
			SET next_attempt_at = clock_timestamp() + INTERVAL '1 hour'
			WHERE id = 'future'
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE outbox_events
			SET dispatched_at = clock_timestamp()
			WHERE id = 'delivered'
		`); err != nil {
			t.Fatal(err)
		}
		calls := 0
		d := newDispatcher(t, enqueueFunc(func(callCtx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
			calls++
			if taskID(opts) != "due" || task.Type() != "media:validate" {
				t.Fatal("task ID/type do not match stored event")
			}
			var payload map[string]string
			if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload["assetId"] != "owned" {
				t.Fatal("task payload changed")
			}
			deadline, ok := callCtx.Deadline()
			if !ok || time.Until(deadline) > options.EnqueueTimeout {
				t.Fatal("enqueue needs a bounded context")
			}
			foundQueue := false
			for _, opt := range opts {
				if opt.Type() == asynq.QueueOpt && opt.Value() == "validation" {
					foundQueue = true
				}
			}
			if !foundQueue {
				t.Fatal("stored queue was not forwarded")
			}
			return nil, nil
		}))
		if result, err := d.DispatchOne(ctx); err != nil || result.EventID != "" || calls != 0 {
			t.Fatal("ineligible events were dispatched")
		}
		insert(t, "due", "validation")
		if result, err := d.DispatchOne(ctx); err != nil || result.EventID != "due" || !result.Delivered {
			t.Fatalf("dispatch failed: %+v %v", result, err)
		}
		delivered, attempts, code, _ := state(t, "due")
		if !delivered || attempts != 1 || code != "" || calls != 1 {
			t.Fatal("success bookkeeping is incorrect")
		}
	})

	t.Run("competing dispatchers skip an actively locked event", func(t *testing.T) {
		reset(t)
		insert(t, "first", "validation")
		insert(t, "second", "validation")
		started := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		d1 := newDispatcher(t, enqueueFunc(func(callCtx context.Context, _ *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
			if taskID(opts) != "first" {
				return nil, errors.New("unexpected first event")
			}
			close(started)
			select {
			case <-release:
				return nil, nil
			case <-callCtx.Done():
				return nil, callCtx.Err()
			}
		}))
		done := make(chan error, 1)
		go func() {
			result, err := d1.DispatchOne(ctx)
			if err == nil && !result.Delivered {
				err = errors.New("first event was not delivered")
			}
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("first dispatcher did not acquire a row")
		}
		if result, err := newDispatcher(t, accept).DispatchOne(ctx); err != nil || result.EventID != "second" || !result.Delivered {
			t.Fatalf("second dispatcher did not skip locked row: %+v %v", result, err)
		}
		unblock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failure commits safe backoff and success clears the error", func(t *testing.T) {
		reset(t)
		insert(t, "retry", "validation")
		d := newDispatcher(t, enqueueFunc(func(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
			return nil, errors.New("redis://fake-secret:password@private.invalid payload-private")
		}))
		var logs bytes.Buffer
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		d.Run(runCtx, slog.New(slog.NewTextHandler(&logs, nil)))
		delivered, attempts, code, future := state(t, "retry")
		if delivered || attempts != 1 || code != "ENQUEUE_FAILED" || !future {
			t.Fatalf("failed event lost retry state: %t %d %s %t", delivered, attempts, code, future)
		}
		if strings.Contains(logs.String(), "fake-secret") || strings.Contains(logs.String(), "payload-private") {
			t.Fatal("raw Redis error reached logs")
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE outbox_events
			SET next_attempt_at = clock_timestamp() - INTERVAL '1 second'
			WHERE id = 'retry'
		`); err != nil {
			t.Fatal(err)
		}
		if result, err := newDispatcher(t, accept).DispatchOne(ctx); err != nil || !result.Delivered {
			t.Fatalf("retry failed: %+v %v", result, err)
		}
		delivered, attempts, code, _ = state(t, "retry")
		if !delivered || attempts != 2 || code != "" {
			t.Fatal("successful retry did not clear previous error")
		}
	})

	t.Run("enqueue timeout saves a retry", func(t *testing.T) {
		reset(t)
		insert(t, "timeout", "validation")
		d := newDispatcher(t, enqueueFunc(func(callCtx context.Context, _ *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
			<-callCtx.Done()
			return nil, callCtx.Err()
		}))
		result, err := d.DispatchOne(ctx)
		if err != nil || result.ErrorCode != "ENQUEUE_TIMEOUT" || result.Delivered {
			t.Fatalf("timeout not recorded: %+v %v", result, err)
		}
		delivered, attempts, code, future := state(t, "timeout")
		if delivered || attempts != 1 || code != "ENQUEUE_TIMEOUT" || !future {
			t.Fatal("enqueue timeout did not commit retry bookkeeping")
		}
	})

	t.Run("cancellation after enqueue rolls back and releases the lock", func(t *testing.T) {
		reset(t)
		insert(t, "cancel", "validation")
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		d := newDispatcher(t, enqueueFunc(func(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
			cancel()
			return nil, nil
		}))
		if _, err := d.DispatchOne(cancelCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
		delivered, attempts, _, _ := state(t, "cancel")
		if delivered || attempts != 0 {
			t.Fatal("cancelled transaction committed bookkeeping")
		}
		// A new dispatcher must reclaim it; conflict confirms prior Redis acceptance.
		d = newDispatcher(t, enqueueFunc(func(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
			return nil, asynq.ErrTaskIDConflict
		}))
		if result, err := d.DispatchOne(ctx); err != nil || !result.Delivered {
			t.Fatalf("recovery failed: %+v %v", result, err)
		}
	})

	t.Run("DB failure after acceptance leaves the event recoverable", func(t *testing.T) {
		reset(t)
		insert(t, "db-failure", "validation")
		if _, err := pool.ExecContext(ctx, `
			ALTER TABLE outbox_events
			ADD CONSTRAINT test_dispatch_failure CHECK (dispatch_attempts = 0)
		`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := pool.ExecContext(ctx, `
				ALTER TABLE outbox_events
				DROP CONSTRAINT IF EXISTS test_dispatch_failure
			`); err != nil {
				t.Error("could not remove test-only dispatcher constraint")
			}
		})
		if _, err := newDispatcher(t, accept).DispatchOne(ctx); !errors.Is(err, job.ErrOutboxDatabase) {
			t.Fatalf("expected safe DB failure, got %v", err)
		}
		delivered, attempts, _, _ := state(t, "db-failure")
		if delivered || attempts != 0 {
			t.Fatal("failed transaction did not roll back")
		}
	})

	t.Run("real Redis stable ID survives repeated dispatch", func(t *testing.T) {
		raw := os.Getenv("TEST_ASYNQ_REDIS_URL")
		if raw == "" {
			t.Skip("set TEST_ASYNQ_REDIS_URL to an isolated Redis instance; this test enqueues tasks")
		}
		reset(t)
		t.Setenv("REDIS_URL", raw)
		redisConfig, err := config.LoadRedis()
		if err != nil {
			t.Fatal(err)
		}
		client := job.NewClient(redisConfig)
		defer client.Close()
		inspector := asynq.NewInspector(redisConfig.Options)
		defer inspector.Close()
		id := rand.Text()
		queue := "outbox-test-" + rand.Text()
		insert(t, id, queue)
		d := newDispatcher(t, client)
		if result, err := d.DispatchOne(ctx); err != nil || !result.Delivered {
			t.Fatalf("real enqueue failed: %+v %v", result, err)
		}
		const simulateUnconfirmedCommitQuery = `
			UPDATE outbox_events
			SET dispatched_at = NULL
			WHERE id = $1
		`
		if _, err := pool.ExecContext(ctx, simulateUnconfirmedCommitQuery, id); err != nil {
			t.Fatal(err)
		}
		if result, err := d.DispatchOne(ctx); err != nil || !result.Delivered {
			t.Fatalf("task ID conflict recovery failed: %+v %v", result, err)
		}
		info, err := inspector.GetTaskInfo(queue, id)
		if err != nil || info.Type != "media:validate" {
			t.Fatal("stored Asynq task was not found")
		}
		queueInfo, err := inspector.GetQueueInfo(queue)
		if err != nil || queueInfo.Pending != 1 {
			t.Fatal("duplicate dispatch created another pending task")
		}
		delivered, attempts, _, _ := state(t, id)
		if !delivered || attempts != 2 {
			t.Fatal("real Redis delivery was not confirmed in PostgreSQL")
		}
	})
}
