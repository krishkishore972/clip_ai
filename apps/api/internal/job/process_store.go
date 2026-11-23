package job

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

const (
	ProcessTaskType = "media:process"
	ProcessQueue    = "processing"
	ProcessAttempts = preparation.MaxAttempts
	ProcessMaxRetry = ProcessAttempts - 1

	processDatabaseTimeout = 5 * time.Second
)

var (
	ErrProcessDatabase  = errors.New("media process database operation failed")
	ErrProcessBusy      = errors.New("media process claim is busy")
	ErrProcessStale     = errors.New("media process claim is stale")
	ErrProcessArguments = errors.New("invalid media process arguments")
)

type ProcessAsset struct {
	ID        string
	Bucket    string
	Key       string
	Token     string
	OutputID  string
	SizeBytes int64
	Attempts  int
}

type ProcessOutcome struct {
	AssetStatus string
	PrepStatus  string
	Code        string
	Metadata    *media.Metadata
	Artifacts   *preparation.Artifacts
}

type ProcessStore interface {
	Claim(context.Context, string, string, time.Duration) (ProcessAsset, error)
	Finish(context.Context, ProcessAsset, ProcessOutcome) error
	Recover(context.Context, int) (int, error)
}

// SQLProcessStore is the single writer of combined validation/preparation
// attempt transitions. Preparation status reads and legacy ensure requests
// remain in preparation.SQLStore.
type SQLProcessStore struct {
	DB *sql.DB
	// Empty disables automatic transcription. Nonempty is snapshotted per job.
	TranscriptionLanguage string
}

func processDBError(parent, operation context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := operation.Err(); err != nil {
		return err
	}
	return ErrProcessDatabase
}

func processEvent(ctx context.Context, tx *sql.Tx, assetID, dedupe string, next time.Duration) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ErrProcessDatabase
	}
	payload, err := json.Marshal(struct {
		AssetID string `json:"assetId"`
	}{assetID})
	if err != nil {
		return ErrProcessDatabase
	}
	const query = `
		INSERT INTO outbox_events (
		    id,
		    asset_id,
		    task_type,
		    queue_name,
		    payload,
		    dedupe_key,
		    next_attempt_at
		)
		VALUES (
		    $1, $2, $3, $4, $5, $6,
		    clock_timestamp() + $7 * INTERVAL '1 microsecond'
		)
		ON CONFLICT (dedupe_key) DO NOTHING
	`
	_, err = tx.ExecContext(
		ctx,
		query,
		hex.EncodeToString(id[:]),
		assetID,
		ProcessTaskType,
		ProcessQueue,
		payload,
		dedupe,
		next.Microseconds(),
	)
	if err != nil {
		return processDBError(ctx, ctx)
	}
	return nil
}
