package job

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/hibiken/asynq"
)

var ErrOutboxDatabase = errors.New("outbox database operation failed")

// TaskEnqueuer is implemented by the real Asynq client and offline test doubles.
type TaskEnqueuer interface {
	EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)
}

type DispatcherOptions struct {
	PollInterval       time.Duration
	TransactionTimeout time.Duration
	EnqueueTimeout     time.Duration
	RetryBase          time.Duration
	RetryMax           time.Duration
}

func (o DispatcherOptions) Validate() error {
	if o.PollInterval < time.Millisecond ||
		o.EnqueueTimeout < time.Millisecond ||
		o.RetryBase < time.Millisecond ||
		o.RetryMax < o.RetryBase ||
		o.TransactionTimeout <= o.EnqueueTimeout {
		return errors.New("outbox durations must be at least 1ms, transaction timeout must exceed enqueue timeout, and retry max must not be less than retry base")
	}
	return nil
}

type DispatchResult struct {
	EventID   string
	Attempts  int
	Delivered bool
	ErrorCode string
}

type Dispatcher struct {
	db      *sql.DB
	client  TaskEnqueuer
	options DispatcherOptions
}

func NewDispatcher(database *sql.DB, client TaskEnqueuer, options DispatcherOptions) (*Dispatcher, error) {
	if database == nil || client == nil {
		return nil, errors.New("outbox database and enqueue client are required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &Dispatcher{db: database, client: client, options: options}, nil
}

// DispatchOne holds one row lock until Redis delivery and its bookkeeping have
// finished. No row (empty EventID) is normal. A committed delivery failure is
// returned in ErrorCode; err instead means DB work could not be confirmed.
func (d *Dispatcher) DispatchOne(ctx context.Context) (DispatchResult, error) {
	txCtx, cancel := context.WithTimeout(ctx, d.options.TransactionTimeout)
	defer cancel()
	tx, err := d.db.BeginTx(txCtx, nil)
	if err != nil {
		return DispatchResult{}, outboxError(ctx, txCtx)
	}
	defer tx.Rollback()

	const claimQuery = `
		SELECT
			id,
			task_type,
			queue_name,
			payload,
			dispatch_attempts
		FROM outbox_events
		WHERE dispatched_at IS NULL
			AND next_attempt_at <= CURRENT_TIMESTAMP
		ORDER BY next_attempt_at, created_at, id
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`

	var eventID, taskType, queue string
	var payload []byte
	var attempts int
	err = tx.QueryRowContext(txCtx, claimQuery).Scan(
		&eventID,
		&taskType,
		&queue,
		&payload,
		&attempts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DispatchResult{}, nil
	}
	if err != nil {
		return DispatchResult{}, outboxError(ctx, txCtx)
	}

	result := DispatchResult{EventID: eventID, Attempts: attempts + 1}
	enqueueCtx, stopEnqueue := context.WithTimeout(txCtx, d.options.EnqueueTimeout)
	_, enqueueErr := d.client.EnqueueContext(
		enqueueCtx,
		asynq.NewTask(taskType, payload),
		asynq.Queue(queue),
		asynq.TaskID(eventID),
	)
	enqueueDeadline := enqueueCtx.Err()
	stopEnqueue()
	if txCtx.Err() != nil {
		return result, outboxError(ctx, txCtx)
	}

	// Event IDs are reserved by this outbox producer. A task-ID conflict on
	// its queue means an earlier enqueue succeeded before DB confirmation.
	if enqueueErr == nil || errors.Is(enqueueErr, asynq.ErrTaskIDConflict) {
		const deliveredQuery = `
			UPDATE outbox_events
			SET
				dispatched_at = clock_timestamp(),
				dispatch_attempts = dispatch_attempts + 1,
				last_error_code = NULL
			WHERE id = $1
		`
		if _, err := tx.ExecContext(txCtx, deliveredQuery, eventID); err != nil {
			return result, outboxError(ctx, txCtx)
		}
		result.Delivered = true
	} else {
		result.ErrorCode = "ENQUEUE_FAILED"
		if errors.Is(enqueueDeadline, context.DeadlineExceeded) || errors.Is(enqueueErr, context.DeadlineExceeded) {
			result.ErrorCode = "ENQUEUE_TIMEOUT"
		}
		const retryQuery = `
			UPDATE outbox_events
			SET
				dispatch_attempts = dispatch_attempts + 1,
				next_attempt_at = clock_timestamp() + $2 * INTERVAL '1 microsecond',
				last_error_code = $3
			WHERE id = $1
		`
		delay := retryDelay(result.Attempts, d.options.RetryBase, d.options.RetryMax)
		if _, err := tx.ExecContext(
			txCtx,
			retryQuery,
			eventID,
			delay.Microseconds(),
			result.ErrorCode,
		); err != nil {
			return result, outboxError(ctx, txCtx)
		}
	}
	if err := tx.Commit(); err != nil {
		// Even after an error Redis may contain the task. Never claim an
		// exactly-once outcome; the same event ID will be retried if needed.
		result.Delivered = false
		return result, outboxError(ctx, txCtx)
	}
	return result, nil
}

// Run drains due work, waits when idle or on a DB error, and stops on context
// cancellation. In-flight cancellation rolls back instead of leaving a lease.

/*
Run starts
│
├─ DispatchOne picks A
│  ├─ Queues A in Redis
│  └─ Marks A dispatched in PostgreSQL
├─ Logs success
├─ continue → no polling delay
│
├─ DispatchOne picks B
│  ├─ Redis enqueue fails
│  └─ Saves a future retry time in PostgreSQL
├─ Logs warning
├─ continue → no polling delay
│
├─ DispatchOne finds nothing currently due
├─ Starts 2-second timer
│
├─ Timer fires
├─ Checks again
│
└─ Eventually cancellation is requested
   └─ Run returns
*/

func (d *Dispatcher) Run(ctx context.Context, logger *slog.Logger) {
	for ctx.Err() == nil {
		result, err := d.DispatchOne(ctx)
		// DispatchOne can take time. Shutdown might be requested during its database or Redis work.
		if ctx.Err() != nil {
			return
		}

		switch {
		case err != nil:
			logger.Error("outbox dispatch database failure", "code", "OUTBOX_DATABASE")
		case result.EventID == "":
			// Nothing due; wait below.
		case result.Delivered:
			logger.Info("outbox event dispatched", "event_id", result.EventID, "attempts", result.Attempts)
			continue
		default:
			logger.Warn("outbox event deferred", "event_id", result.EventID, "attempts", result.Attempts, "code", result.ErrorCode)
			continue
		}
		timer := time.NewTimer(d.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func outboxError(parent, transaction context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := transaction.Err(); err != nil {
		return err
	}
	// DB and queue errors may contain credentials, payloads or connection data.
	return ErrOutboxDatabase
}

func retryDelay(attempt int, base, ceiling time.Duration) time.Duration {
	delay := base
	for i := 1; i < attempt && delay < ceiling; i++ {
		if delay > ceiling/2 {
			delay = ceiling
			break
		}
		delay *= 2
	}
	// Equal jitter: [half of capped exponential delay, full delay].
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(delay-half)+1))
}
