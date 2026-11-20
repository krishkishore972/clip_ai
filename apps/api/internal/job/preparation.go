package job

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

const PreparationMaxRetry = 1

type PreparationStore interface {
	Claim(context.Context, string, string, time.Duration) (preparation.Claim, error)
	Finish(context.Context, preparation.Claim, preparation.Outcome) error
	Recover(context.Context, int) (int, error)
}

type DerivedUploader interface {
	UploadDerived(context.Context, string, string, string, string) (storage.DerivedObject, error)
}

type PreparationOptions struct {
	Bucket, TempDir                                            string
	DownloadTimeout, EncodeTimeout, UploadTimeout, TaskTimeout time.Duration
	Prepare                                                    func(context.Context, string, string, time.Duration) (media.PreparedMedia, error)
}

type PreparationHandler struct {
	store         PreparationStore
	downloader    VideoDownloader
	uploader      DerivedUploader
	options       PreparationOptions
	logger        *slog.Logger
	inspector     ValidationTaskInspector
	archiveCursor string
	mu            sync.Mutex
	closing       bool
	active        sync.WaitGroup
	serial        chan struct{}
	cancel        context.CancelFunc
	lifetime      context.Context
}

func NewPreparationHandler(store PreparationStore, downloader VideoDownloader, uploader DerivedUploader, options PreparationOptions, logger *slog.Logger) (*PreparationHandler, error) {
	if store == nil || downloader == nil || uploader == nil || logger == nil || options.Bucket == "" || options.DownloadTimeout <= 0 || options.EncodeTimeout <= 0 || options.UploadTimeout <= 0 || options.TaskTimeout <= options.DownloadTimeout+options.EncodeTimeout+options.UploadTimeout {
		return nil, errors.New("preparation: invalid worker options")
	}
	root, err := prepareValidationRoot(options.TempDir)
	if err != nil {
		return nil, errors.New("preparation: private temporary root unavailable")
	}
	options.TempDir = root
	if options.Prepare == nil {
		options.Prepare = media.PrepareFile
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &PreparationHandler{store: store, downloader: downloader, uploader: uploader, options: options, logger: logger, serial: make(chan struct{}, 1), lifetime: lifetime, cancel: cancel}, nil
}

func preparationAssetID(task *asynq.Task) (string, error) {
	if task == nil || task.Type() != preparation.TaskType || len(task.Payload()) > 1024 {
		return "", errors.New("invalid preparation task")
	}
	var input struct {
		AssetID string `json:"assetId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(task.Payload()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !ValidValidationAssetID(input.AssetID) {
		return "", errors.New("invalid preparation task")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.New("invalid preparation task")
	}
	return input.AssetID, nil
}

func preparationTaskError(code string, stop bool) error {
	if stop {
		return fmt.Errorf("preparation: %s: %w", code, asynq.SkipRetry)
	}
	return fmt.Errorf("preparation: %s", code)
}

func (h *PreparationHandler) ProcessTask(parent context.Context, task *asynq.Task) error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return preparationTaskError("WORKER_SHUTDOWN", false)
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	id, err := preparationAssetID(task)
	if err != nil {
		return preparationTaskError("INVALID_PREPARATION_TASK", true)
	}
	ctx, cancel := context.WithTimeout(parent, h.options.TaskTimeout)
	defer cancel()
	stopLifetime := context.AfterFunc(h.lifetime, cancel)
	defer stopLifetime()
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-ctx.Done():
		return preparationTaskError("PREPARATION_INTERRUPTED", false)
	}
	deadline, _ := ctx.Deadline()
	reserve := min(10*time.Second, h.options.TaskTimeout/10)
	workCtx, stopWork := context.WithDeadline(ctx, deadline.Add(-reserve))
	defer stopWork()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return preparationTaskError("PREPARATION_UNAVAILABLE", false)
	}
	claim, err := h.store.Claim(workCtx, id, hex.EncodeToString(random[:]), h.options.TaskTimeout+time.Minute)
	if errors.Is(err, preparation.ErrBusy) {
		return nil
	}
	if err != nil {
		return preparationTaskError("PREPARATION_DATABASE", false)
	}
	if claim.AssetID == "" {
		return nil
	}
	outcome, operationErr := h.prepare(workCtx, claim)
	if workCtx.Err() != nil {
		outcome = preparation.Outcome{Status: "pending", Code: "PREPARATION_INTERRUPTED"}
		operationErr = workCtx.Err()
		if errors.Is(operationErr, context.DeadlineExceeded) {
			outcome.Code = "PREPARATION_TIMEOUT"
		}
	}
	terminal := claim.Attempts >= preparation.MaxAttempts
	if operationErr != nil {
		outcome.Status = "pending"
		if terminal {
			outcome.Status = "failed"
		}
	}
	finishCtx, stopFinish := context.WithDeadline(context.WithoutCancel(parent), minTime(deadline, time.Now().Add(5*time.Second)))
	defer stopFinish()
	if err := h.store.Finish(finishCtx, claim, outcome); errors.Is(err, preparation.ErrStale) {
		return nil
	} else if err != nil {
		return preparationTaskError("PREPARATION_DATABASE", terminal)
	}
	h.logger.Info("preparation attempt finished", "asset_id", id, "attempts", claim.Attempts, "status", outcome.Status, "code", outcome.Code)
	if operationErr != nil {
		return preparationTaskError(outcome.Code, terminal)
	}
	if outcome.Status == "failed" {
		return preparationTaskError(outcome.Code, true)
	}
	return nil
}

func (h *PreparationHandler) prepare(ctx context.Context, claim preparation.Claim) (preparation.Outcome, error) {
	fail := func(code string, err error) (preparation.Outcome, error) {
		return preparation.Outcome{Status: "pending", Code: code}, err
	}
	if claim.Bucket != h.options.Bucket || claim.Key == "" {
		return fail("PREPARATION_STORAGE_FAILED", errors.New("storage mismatch"))
	}
	if claim.SizeBytes <= 0 || claim.SizeBytes > media.MaxFileBytes {
		return preparation.Outcome{Status: "failed", Code: "PREPARATION_INVALID_OUTPUT"}, nil
	}
	directory, err := h.newAttemptDirectory()
	if err != nil {
		return fail("PREPARATION_DISK_SPACE", err)
	}
	defer h.RemoveAttemptDirectory(directory)
	source := filepath.Join(directory, "source")
	downloadCtx, cancel := context.WithTimeout(ctx, h.options.DownloadTimeout)
	err = h.downloader.Download(downloadCtx, claim.Key, source, claim.SizeBytes)
	cancel()
	if err != nil {
		return fail("PREPARATION_DOWNLOAD_FAILED", err)
	}
	before, err := hashPreparationFile(ctx, source, claim.SizeBytes, directory)
	if err != nil {
		return fail("PREPARATION_SOURCE_CHANGED", err)
	}
	encodeCtx, stopEncode := context.WithTimeout(ctx, h.options.EncodeTimeout)
	prepared, err := h.options.Prepare(encodeCtx, source, directory, h.options.EncodeTimeout)
	stopEncode()
	if err != nil {
		var rejection *media.Rejection
		if errors.As(err, &rejection) {
			return preparation.Outcome{Status: "failed", Code: preparation.SafeFailureCode(rejection.Code)}, nil
		}
		return fail("PREPARATION_ENCODE_FAILED", err)
	}
	after, err := hashPreparationFile(ctx, source, claim.SizeBytes, directory)
	if err != nil || before != after || before != prepared.SourceSHA256 {
		return preparation.Outcome{Status: "failed", Code: "PREPARATION_SOURCE_CHANGED"}, nil
	}
	if prepared.Audio.SizeBytes <= 0 || prepared.Audio.SizeBytes > media.MaxPreparedAudioBytes ||
		prepared.Audio.SampleRate != 16000 || prepared.Audio.Channels != 1 ||
		prepared.Audio.DurationMs <= 0 || prepared.Audio.DurationMs > media.MaxDurationSeconds*1000+100 {
		return preparation.Outcome{Status: "failed", Code: "PREPARATION_INVALID_OUTPUT"}, nil
	}
	audioHash, err := hashPreparationFile(ctx, prepared.Audio.Path, prepared.Audio.SizeBytes, directory)
	if err != nil || audioHash != prepared.Audio.SHA256 || prepared.Audio.Path == source {
		return fail("PREPARATION_INVALID_OUTPUT", errors.New("invalid audio"))
	}
	uploadCtx, stop := context.WithTimeout(ctx, h.options.UploadTimeout)
	defer stop()
	audio, err := h.uploader.UploadDerived(uploadCtx, claim.AssetID, claim.OutputID, "audio", prepared.Audio.Path)
	if err != nil {
		return fail("PREPARATION_UPLOAD_FAILED", err)
	}
	if audio.Key == "" || audio.Key == claim.Key || audio.SizeBytes != prepared.Audio.SizeBytes || audio.SHA256 != audioHash {
		return fail("PREPARATION_INVALID_OUTPUT", errors.New("invalid uploaded output"))
	}
	return preparation.Outcome{
		Status: "ready",
		Artifacts: &preparation.Artifacts{
			Bucket:      claim.Bucket,
			AudioKey:    audio.Key,
			AudioSHA256: audioHash, SourceSHA256: before,
			AudioSizeBytes: audio.SizeBytes,
		},
	}, nil
}

func hashPreparationFile(ctx context.Context, path string, size int64, root string) (string, error) {
	if filepath.Dir(path) != root || !filepath.IsAbs(path) {
		return "", errors.New("invalid output path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return "", errors.New("invalid output file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("output unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("output read failed")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Drain cancels and joins all in-process work after queue consumption stops.
func (h *PreparationHandler) Drain(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	h.cancel()
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
