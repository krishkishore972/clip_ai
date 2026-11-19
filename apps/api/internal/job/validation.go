package job

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

// VideoDownloader reads only from the worker's configured private bucket.
type VideoDownloader interface {
	Download(context.Context, string, string, int64) error
}

type ValidationOptions struct {
	Bucket          string
	TempDir         string
	DownloadTimeout time.Duration
	ProbeTimeout    time.Duration
	TaskTimeout     time.Duration
	// Probe defaults to media.ProbeFile when nil. Set before constructing the handler.
	Probe func(context.Context, string, time.Duration) (media.Metadata, error)
}

type ValidationHandler struct {
	store         ValidationStore
	downloader    VideoDownloader
	options       ValidationOptions
	logger        *slog.Logger
	inspector     ValidationTaskInspector
	archiveCursor string
	mu            sync.Mutex
	closing       bool
	active        sync.WaitGroup
	serial        chan struct{}
}

func NewValidationHandler(store ValidationStore, downloader VideoDownloader, options ValidationOptions, logger *slog.Logger) (*ValidationHandler, error) {
	if store == nil || downloader == nil || logger == nil || options.Bucket == "" ||
		options.DownloadTimeout <= 0 || options.ProbeTimeout <= 0 ||
		options.TaskTimeout <= options.DownloadTimeout+options.ProbeTimeout {
		return nil, errors.New("validation: invalid worker options")
	}
	root, err := prepareValidationRoot(options.TempDir)
	if err != nil {
		return nil, err
	}
	options.TempDir = root
	if options.Probe == nil {
		options.Probe = media.ProbeFile
	}
	return &ValidationHandler{
		store: store, downloader: downloader, options: options, logger: logger,
		serial: make(chan struct{}, 1),
	}, nil
}

var validationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// ValidValidationAssetID reports whether an opaque validation asset ID is safe.
func ValidValidationAssetID(id string) bool {
	return validationIDPattern.MatchString(id)
}

func validationAssetID(task *asynq.Task) (string, error) {
	if task == nil || task.Type() != ValidationTaskType || len(task.Payload()) > 1024 {
		return "", errors.New("invalid validation payload")
	}
	var input struct {
		AssetID string `json:"assetId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(task.Payload()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !ValidValidationAssetID(input.AssetID) {
		return "", errors.New("invalid validation payload")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.New("invalid validation payload")
	}
	return input.AssetID, nil
}

// ProcessTask is cooperative and deadline-bounded. PostgreSQL fencing prevents
// an old invocation from publishing results after its lease has been replaced.
func (h *ValidationHandler) ProcessTask(parent context.Context, task *asynq.Task) error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return validationTaskError("WORKER_SHUTDOWN", false)
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	id, err := validationAssetID(task)
	if err != nil {
		return validationTaskError("INVALID_VALIDATION_TASK", true)
	}
	// Asynq can start a replacement before a canceled handler has unwound.
	// Serialize the complete handler (including cleanup) inside this process.
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-parent.Done():
		return validationTaskError("VALIDATION_INTERRUPTED", lastQueueAttempt(parent))
	}
	ctx, cancel := context.WithTimeout(parent, h.options.TaskTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	// Reserve part of the whole-task budget for database completion and cleanup.
	reserve := min(10*time.Second, h.options.TaskTimeout/10)
	workCtx, stopWork := context.WithDeadline(ctx, deadline.Add(-reserve))
	defer stopWork()

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return validationTaskError("VALIDATION_UNAVAILABLE", lastQueueAttempt(parent))
	}
	token := hex.EncodeToString(random[:])
	asset, err := h.store.Claim(workCtx, id, token, h.options.TaskTimeout+time.Minute)
	if errors.Is(err, ErrValidationBusy) {
		// A duplicate can be acknowledged: the live owner finishes, or Recover
		// creates a new durable event after its lease expires, even if it crashes.
		return nil
	}
	if err != nil {
		return validationTaskError("VALIDATION_DATABASE", lastQueueAttempt(parent))
	}
	if asset.ID == "" {
		return nil // Unknown, unowned or already terminal; never trust queue URLs.
	}

	outcome := ValidationOutcome{Status: "ready"}
	var operationErr error
	switch {
	case asset.Bucket != h.options.Bucket || asset.Key == "":
		operationErr = errors.New("storage configuration mismatch")
		outcome.Code = "STORAGE_CONFIGURATION"
	case asset.SizeBytes <= 0 || asset.SizeBytes > media.MaxFileBytes:
		outcome.Status, outcome.Code = "rejected", "FILE_TOO_LARGE"
	default:
		directory, tempErr := h.newAttemptDirectory()
		if tempErr != nil {
			operationErr, outcome.Code = tempErr, "TEMP_STORAGE_UNAVAILABLE"
			break
		}
		defer h.RemoveAttemptDirectory(directory)
		path := filepath.Join(directory, "source")
		downloadCtx, stopDownload := context.WithTimeout(workCtx, h.options.DownloadTimeout)
		downloadErr := h.downloader.Download(downloadCtx, asset.Key, path, asset.SizeBytes)
		stopDownload()
		if downloadErr != nil {
			operationErr = downloadErr
			outcome.Code = "DOWNLOAD_FAILED"
			if errors.Is(downloadErr, storage.ErrDownloadSizeMismatch) {
				operationErr = nil
				outcome.Status, outcome.Code = "rejected", "SIZE_MISMATCH"
			} else if errors.Is(downloadErr, context.DeadlineExceeded) || errors.Is(downloadErr, storage.ErrDownloadTimeout) {
				outcome.Code = "DOWNLOAD_TIMEOUT"
			} else if errors.Is(downloadErr, storage.ErrObjectNotFound) {
				outcome.Code = "SOURCE_NOT_FOUND"
			}
			break
		}
		metadata, probeErr := h.options.Probe(workCtx, path, h.options.ProbeTimeout)
		if probeErr != nil {
			var rejection *media.Rejection
			if errors.As(probeErr, &rejection) && validationCodePattern.MatchString(rejection.Code) {
				outcome.Status, outcome.Code = "rejected", rejection.Code
			} else {
				operationErr, outcome.Code = probeErr, "PROBE_FAILED"
				if errors.Is(probeErr, media.ErrProbeTimeout) {
					outcome.Code = "PROBE_TIMEOUT"
				} else if errors.Is(probeErr, media.ErrProbeUnavailable) {
					outcome.Code = "PROBE_UNAVAILABLE"
				}
			}
			break
		}
		if metadata.SizeBytes != asset.SizeBytes {
			outcome.Status, outcome.Code = "rejected", "SIZE_MISMATCH"
			break
		}
		outcome.Metadata = &metadata
	}

	if workCtx.Err() != nil {
		operationErr = workCtx.Err()
		outcome.Metadata = nil
		outcome.Code = "VALIDATION_INTERRUPTED"
		if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
			outcome.Code = "VALIDATION_TIMEOUT"
		}
	}
	terminalFailure := operationErr != nil && (asset.Attempts >= ValidationMaxAttempts || lastQueueAttempt(parent))
	if operationErr != nil {
		outcome.Status = "uploaded_unvalidated"
		if terminalFailure {
			outcome.Status = "validation_failed"
		}
	}
	// Cancellation must not prevent bookkeeping, but never extend the original
	// whole-task deadline. If DB remains unavailable, the durable lease recovers.
	finishDeadline := minTime(deadline, time.Now().Add(5*time.Second))
	finishCtx, stopFinish := context.WithDeadline(context.WithoutCancel(parent), finishDeadline)
	defer stopFinish()
	err = h.store.Finish(finishCtx, asset, outcome)
	if errors.Is(err, ErrValidationStale) {
		return nil // A newer fenced attempt owns the outcome.
	}
	if err != nil {
		h.logger.Error("validation result persistence failed", "asset_id", id, "code", "VALIDATION_DATABASE")
		return validationTaskError("VALIDATION_DATABASE", terminalFailure || lastQueueAttempt(parent))
	}
	h.logger.Info("validation attempt finished", "asset_id", id, "attempts", asset.Attempts, "status", outcome.Status, "code", outcome.Code)
	if operationErr != nil {
		return validationTaskError(outcome.Code, terminalFailure)
	}
	return nil
}

func lastQueueAttempt(ctx context.Context) bool {
	retried, ok := asynq.GetRetryCount(ctx)
	if !ok {
		return false
	}
	cap := ValidationMaxRetry
	if native, ok := asynq.GetMaxRetry(ctx); ok {
		cap = min(cap, native)
	}
	return retried >= cap
}

func validationTaskError(code string, stop bool) error {
	if stop {
		return fmt.Errorf("validation: %s: %w", code, asynq.SkipRetry)
	}
	return fmt.Errorf("validation: %s", code)
}

// Drain prevents new invocations and joins handlers that Asynq may have
// canceled without joining. Call only after stopping queue consumption.
func (h *ValidationHandler) Drain(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		h.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
