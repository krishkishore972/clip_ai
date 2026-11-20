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
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

type ProcessOptions struct {
	Bucket, TempDir                                            string
	DownloadTimeout, EncodeTimeout, UploadTimeout, TaskTimeout time.Duration
	Prepare                                                    func(context.Context, string, string, time.Duration) (media.PreparedMedia, error)
}

type ProcessHandler struct {
	store      ProcessStore
	downloader VideoDownloader
	uploader   DerivedUploader
	options    ProcessOptions
	logger     *slog.Logger
	serial     chan struct{}
	active     sync.WaitGroup
	closing    bool
	cancel     context.CancelFunc
	lifetime   context.Context
}

func NewProcessHandler(store ProcessStore, downloader VideoDownloader, uploader DerivedUploader, options ProcessOptions, logger *slog.Logger) (*ProcessHandler, error) {
	if store == nil || downloader == nil || uploader == nil || logger == nil || options.Bucket == "" || options.DownloadTimeout <= 0 || options.EncodeTimeout <= 0 || options.UploadTimeout <= 0 || options.TaskTimeout <= options.DownloadTimeout+options.EncodeTimeout+options.UploadTimeout {
		return nil, errors.New("media process: invalid worker options")
	}
	root, err := prepareProcessRoot(options.TempDir)
	if err != nil {
		return nil, errors.New("media process: private temporary root unavailable")
	}
	options.TempDir = root
	if options.Prepare == nil {
		options.Prepare = media.PrepareFile
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &ProcessHandler{store: store, downloader: downloader, uploader: uploader, options: options, logger: logger, serial: make(chan struct{}, 1), lifetime: lifetime, cancel: cancel}, nil
}

func processAssetID(task *asynq.Task) (string, error) {
	if task == nil || (task.Type() != ValidationTaskType && task.Type() != ProcessTaskType) || len(task.Payload()) > 1024 {
		return "", errors.New("invalid media process task")
	}
	var input struct {
		AssetID string `json:"assetId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(task.Payload()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !validProcessAssetID(input.AssetID) {
		return "", errors.New("invalid media process task")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.New("invalid media process task")
	}
	return input.AssetID, nil
}

func (h *ProcessHandler) ProcessTask(parent context.Context, task *asynq.Task) error {
	h.active.Add(1)
	defer h.active.Done()
	id, err := processAssetID(task)
	if err != nil {
		h.logger.Warn("media processing task rejected", "code", "INVALID_TASK")
		return fmt.Errorf("media process: invalid task: %w", asynq.SkipRetry)
	}
	started := time.Now()
	logger := h.logger.With("asset_id", id, "task_type", task.Type())
	logger.Info("media processing task received")
	ctx, cancel := context.WithTimeout(parent, h.options.TaskTimeout)
	defer cancel()
	stopLifetime := context.AfterFunc(h.lifetime, cancel)
	defer stopLifetime()
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-ctx.Done():
		logger.Warn("media processing interrupted", "stage", "waiting", "code", "PROCESS_INTERRUPTED")
		return errors.New("media process interrupted")
	}
	deadline, _ := ctx.Deadline()
	workCtx, stopWork := context.WithDeadline(ctx, deadline.Add(-min(10*time.Second, h.options.TaskTimeout/10)))
	defer stopWork()
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		logger.Error("media processing unavailable", "code", "CLAIM_TOKEN_UNAVAILABLE")
		return errors.New("media process unavailable")
	}
	asset, err := h.store.Claim(workCtx, id, hex.EncodeToString(tokenBytes[:]), h.options.TaskTimeout+time.Minute)
	if errors.Is(err, ErrProcessBusy) {
		logger.Info("media processing skipped", "reason", "claim_busy")
		return nil
	}
	if err != nil {
		logger.Error("media processing claim failed", "code", "PROCESS_DATABASE")
		return errors.New("media process database failure")
	}
	if asset.ID == "" {
		logger.Info("media processing skipped", "reason", "not_eligible")
		return nil
	}
	logger = logger.With("attempt", asset.Attempts)
	logger.Info("media processing started")
	outcome, operationErr := h.process(workCtx, asset, logger)
	if workCtx.Err() != nil {
		outcome = ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "PROCESS_INTERRUPTED"}
		operationErr = workCtx.Err()
	}
	if operationErr != nil && asset.Attempts >= ProcessAttempts {
		outcome.AssetStatus = "validation_failed"
	}
	finishCtx, stopFinish := context.WithDeadline(context.WithoutCancel(parent), minTime(deadline, time.Now().Add(5*time.Second)))
	defer stopFinish()
	if err := h.store.Finish(finishCtx, asset, outcome); errors.Is(err, ErrProcessStale) {
		logger.Warn("media processing result discarded", "reason", "stale_claim", "elapsed_ms", time.Since(started).Milliseconds())
		return nil
	} else if err != nil {
		logger.Error("media processing result persistence failed", "code", "PROCESS_DATABASE", "elapsed_ms", time.Since(started).Milliseconds())
		return errors.New("media process database failure")
	}
	if operationErr != nil {
		logger.Warn("media processing attempt failed", "code", outcome.Code, "status", outcome.AssetStatus,
			"retry_pending", outcome.AssetStatus == "uploaded_unvalidated", "elapsed_ms", time.Since(started).Milliseconds())
		return errors.New("media process attempt failed")
	}
	if outcome.AssetStatus == "rejected" {
		logger.Warn("media processing rejected", "code", outcome.Code, "elapsed_ms", time.Since(started).Milliseconds())
	} else {
		logger.Info("media processing completed", "status", outcome.AssetStatus, "elapsed_ms", time.Since(started).Milliseconds())
	}
	return nil
}

func (h *ProcessHandler) process(ctx context.Context, asset ProcessAsset, logger *slog.Logger) (ProcessOutcome, error) {
	directory, err := h.newProcessDirectory()
	if err != nil {
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "TEMP_STORAGE_UNAVAILABLE"}, err
	}
	defer os.RemoveAll(directory)
	source := filepath.Join(directory, "source")
	stageStarted := time.Now()
	logger.Info("media processing stage started", "stage", "download")
	downloadCtx, stopDownload := context.WithTimeout(ctx, h.options.DownloadTimeout)
	err = h.downloader.Download(downloadCtx, asset.Key, source, asset.SizeBytes)
	stopDownload()
	if err != nil {
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "DOWNLOAD_FAILED"}, err
	}
	logger.Info("media processing stage completed", "stage", "download", "elapsed_ms", time.Since(stageStarted).Milliseconds())
	before, err := hashPreparationFile(ctx, source, asset.SizeBytes, directory)
	if err != nil {
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "SOURCE_CHANGED"}, err
	}
	stageStarted = time.Now()
	logger.Info("media processing stage started", "stage", "validation")
	metadata, err := media.ProbeFile(ctx, source, time.Minute)
	if err != nil {
		var rejection *media.Rejection
		if errors.As(err, &rejection) {
			return ProcessOutcome{AssetStatus: "rejected", PrepStatus: "failed", Code: rejection.Code}, nil
		}
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "PROBE_FAILED"}, err
	}
	if metadata.SizeBytes != asset.SizeBytes || !validStoredMetadata(&metadata) {
		return ProcessOutcome{AssetStatus: "rejected", PrepStatus: "failed", Code: "INVALID_MEDIA"}, nil
	}
	logger.Info("media processing stage completed", "stage", "validation", "elapsed_ms", time.Since(stageStarted).Milliseconds())
	stageStarted = time.Now()
	logger.Info("media processing stage started", "stage", "audio_extraction")
	encodeCtx, stopEncode := context.WithTimeout(ctx, h.options.EncodeTimeout)
	prepared, err := h.options.Prepare(encodeCtx, source, directory, h.options.EncodeTimeout)
	stopEncode()
	if err != nil {
		var rejection *media.Rejection
		if errors.As(err, &rejection) {
			return ProcessOutcome{AssetStatus: "rejected", PrepStatus: "failed", Code: rejection.Code}, nil
		}
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "PREPARATION_ENCODE_FAILED"}, err
	}
	after, err := hashPreparationFile(ctx, source, asset.SizeBytes, directory)
	if err != nil || before != after || before != prepared.SourceSHA256 {
		return ProcessOutcome{AssetStatus: "rejected", PrepStatus: "failed", Code: "SOURCE_CHANGED"}, errors.New("source changed")
	}
	audioHash, err := hashPreparationFile(ctx, prepared.Audio.Path, prepared.Audio.SizeBytes, directory)
	if err != nil || audioHash != prepared.Audio.SHA256 {
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "PREPARATION_INVALID_OUTPUT"}, errors.New("invalid audio")
	}
	if prepared.Audio.StartMs < -media.MaxAudioStartMs || prepared.Audio.StartMs > media.MaxAudioStartMs {
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "PREPARATION_INVALID_OUTPUT"}, errors.New("invalid audio timeline")
	}
	logger.Info("media processing stage completed", "stage", "audio_extraction", "elapsed_ms", time.Since(stageStarted).Milliseconds())
	stageStarted = time.Now()
	logger.Info("media processing stage started", "stage", "audio_upload")
	uploadCtx, stopUpload := context.WithTimeout(ctx, h.options.UploadTimeout)
	audio, err := h.uploader.UploadDerived(uploadCtx, asset.ID, asset.OutputID, "audio", prepared.Audio.Path)
	stopUpload()
	if err != nil {
		return ProcessOutcome{AssetStatus: "uploaded_unvalidated", Code: "PREPARATION_UPLOAD_FAILED"}, err
	}
	logger.Info("media processing stage completed", "stage", "audio_upload", "elapsed_ms", time.Since(stageStarted).Milliseconds())
	return ProcessOutcome{
		AssetStatus: "ready",
		PrepStatus:  "ready",
		Metadata:    &metadata,
		Artifacts: &preparation.Artifacts{
			Bucket:         asset.Bucket,
			AudioKey:       audio.Key,
			AudioSHA256:    audioHash,
			SourceSHA256:   before,
			AudioSizeBytes: audio.SizeBytes,
			AudioStartMs:   prepared.Audio.StartMs,
		},
	}, nil
}

func (h *ProcessHandler) newProcessDirectory() (string, error) {
	return os.MkdirTemp(h.options.TempDir, "process-")
}

func (h *ProcessHandler) Drain(ctx context.Context) error {
	h.cancel()
	done := make(chan struct{})
	go func() { h.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *ProcessHandler) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		operation, cancel := context.WithTimeout(ctx, 5*time.Second)
		recovered, err := h.store.Recover(operation, 100)
		cancel()
		if err != nil && ctx.Err() == nil {
			h.logger.Error("media processing recovery failed", "code", "PROCESS_DATABASE")
		} else if recovered > 0 {
			h.logger.Info("media processing expired claims recovered", "count", recovered)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
