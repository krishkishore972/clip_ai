package job

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

const TranscriptionTaskTimeout = 25 * time.Minute

type TranscriptFileStore interface {
	Claim(context.Context, string, string, time.Duration) (transcription.Claim, error)
	Finish(context.Context, transcription.Claim, *transcription.Publication, string, bool) error
	Recover(context.Context, int) (int, error)
}

type AudioTranscriber interface {
	TranscribeFileWithLanguage(context.Context, string, string) (*response.PreRecordedResponse, error)
}

type TranscriptionHandler struct {
	store        TranscriptFileStore
	downloader   VideoDownloader
	uploader     DerivedUploader
	provider     AudioTranscriber
	root, bucket string
	logger       *slog.Logger
	lifetime     context.Context
	cancel       context.CancelFunc
	active       sync.WaitGroup
	serial       chan struct{}
}

func NewTranscriptionHandler(
	store TranscriptFileStore,
	downloader VideoDownloader,
	uploader DerivedUploader,
	provider AudioTranscriber,
	root, bucket string,
	logger *slog.Logger,
) (*TranscriptionHandler, error) {
	if store == nil || downloader == nil || uploader == nil || provider == nil || bucket == "" || logger == nil {
		return nil, errors.New("transcription: worker dependencies are required")
	}
	privateRoot, err := prepareProcessRoot(root)
	if err != nil {
		return nil, errors.New("transcription: private temporary root unavailable")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &TranscriptionHandler{
		store: store, downloader: downloader, uploader: uploader, provider: provider,
		root: privateRoot, bucket: bucket, logger: logger, lifetime: lifetime, cancel: cancel,
		serial: make(chan struct{}, 1),
	}, nil
}

func (h *TranscriptionHandler) ProcessTask(parent context.Context, task *asynq.Task) error {
	if task == nil || task.Type() != transcription.TaskType || len(task.Payload()) > 1024 {
		return asynq.SkipRetry
	}
	var payload struct {
		AssetID string `json:"assetId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(task.Payload()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || !validProcessAssetID(payload.AssetID) {
		return asynq.SkipRetry
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return asynq.SkipRetry
	}
	h.active.Add(1)
	defer h.active.Done()
	ctx, cancel := context.WithTimeout(parent, TranscriptionTaskTimeout)
	defer cancel()
	stop := context.AfterFunc(h.lifetime, cancel)
	defer stop()
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-ctx.Done():
		return errors.New("transcription: task interrupted")
	}
	logger := h.logger.With("asset_id", payload.AssetID, "task_type", transcription.TaskType)
	logger.Info("transcription task received")
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return errors.New("transcription: claim unavailable")
	}
	claim, err := h.store.Claim(ctx, payload.AssetID, hex.EncodeToString(token[:]), 26*time.Minute)
	if errors.Is(err, transcription.ErrBusy) {
		logger.Info("transcription skipped", "reason", "claim_busy")
		return nil
	}
	if err != nil {
		logger.Error("transcription claim failed", "code", "TRANSCRIPTION_DATABASE")
		return errors.New("transcription: claim failed")
	}
	if claim.AssetID == "" {
		logger.Info("transcription skipped", "reason", "not_eligible")
		return nil
	}
	logger = logger.With("attempt", claim.Attempts)
	// Reserve persistence time even when the task or a stage times out.
	deadline, _ := ctx.Deadline()
	work, stopWork := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
	pub, code, retry := h.process(work, claim, logger)
	stopWork()
	if pub == nil && ctx.Err() != nil {
		code, retry = "TRANSCRIPTION_INTERRUPTED", true
	}
	finish, stopFinish := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer stopFinish()
	err = h.store.Finish(finish, claim, pub, code, retry)
	if errors.Is(err, transcription.ErrStale) {
		logger.Warn("transcription publication discarded", "reason", "stale_claim")
		return nil
	}
	if err != nil {
		logger.Error("transcription publication failed", "code", "TRANSCRIPTION_DATABASE")
		return errors.New("transcription: publication failed")
	}
	if pub == nil {
		logger.Warn("transcription attempt failed", "code", code,
			"retry_pending", retry && claim.Attempts < transcription.MaxAttempts)
	} else {
		logger.Info("transcription ready", "word_count", pub.WordCount)
	}
	// Finish committed the outcome and any durable retry; do not race it with
	// a queue retry. Queue retries are only for pre-Finish failures.
	return nil
}

func (h *TranscriptionHandler) process(ctx context.Context, claim transcription.Claim, logger *slog.Logger) (*transcription.Publication, string, bool) {
	if claim.Bucket != h.bucket || !claim.ValidSource() {
		return nil, "TRANSCRIPTION_SOURCE_CHANGED", false
	}
	dir, err := os.MkdirTemp(h.root, "transcribe-")
	if err != nil {
		return nil, "TRANSCRIPTION_STORAGE_FAILED", true
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			logger.Warn("transcription temporary cleanup failed", "code", "TEMP_CLEANUP_FAILED")
		}
	}()
	audio := filepath.Join(dir, "audio.wav")
	logger.Info("transcription stage started", "stage", "download")
	stage, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err = h.downloader.Download(stage, claim.AudioKey, audio, claim.AudioSizeBytes)
	cancel()
	if err != nil {
		return nil, "TRANSCRIPTION_DOWNLOAD_FAILED", true
	}
	sum, err := hashPreparationFile(ctx, audio, claim.AudioSizeBytes, dir)
	if err != nil || sum != claim.AudioSHA256 {
		return nil, "TRANSCRIPTION_SOURCE_CHANGED", false
	}
	logger.Info("transcription stage completed", "stage", "download")
	logger.Info("transcription stage started", "stage", "provider")
	stage, cancel = context.WithTimeout(ctx, 12*time.Minute)
	result, err := h.provider.TranscribeFileWithLanguage(stage, audio, claim.Language)
	cancel()
	if err != nil {
		switch {
		case errors.Is(err, transcription.ErrProviderAuth):
			return nil, "TRANSCRIPTION_PROVIDER_AUTH", false
		case errors.Is(err, transcription.ErrProviderRejected), errors.Is(err, transcription.ErrLanguage):
			return nil, "TRANSCRIPTION_PROVIDER_REJECTED", false
		case errors.Is(err, transcription.ErrResponse):
			return nil, "TRANSCRIPTION_INVALID_OUTPUT", false
		case errors.Is(err, context.DeadlineExceeded):
			return nil, "TRANSCRIPTION_TIMEOUT", true
		default:
			return nil, "TRANSCRIPTION_PROVIDER_FAILED", true
		}
	}
	doc, err := transcription.Normalize(result, claim)
	if err != nil {
		return nil, "TRANSCRIPTION_INVALID_OUTPUT", false
	}
	data, err := doc.JSON()
	if err != nil {
		return nil, "TRANSCRIPTION_INVALID_OUTPUT", false
	}
	logger.Info("transcription stage completed", "stage", "provider")
	output := filepath.Join(dir, "transcript.json")
	if err := os.WriteFile(output, data, 0600); err != nil {
		return nil, "TRANSCRIPTION_STORAGE_FAILED", true
	}
	logger.Info("transcription stage started", "stage", "upload")
	stage, cancel = context.WithTimeout(ctx, 2*time.Minute)
	object, err := h.uploader.UploadDerived(stage, claim.AssetID, claim.OutputID, "transcript", output)
	cancel()
	if err != nil {
		return nil, "TRANSCRIPTION_UPLOAD_FAILED", true
	}
	hash := sha256.Sum256(data)
	if object.Key != transcription.TranscriptKey(claim.AssetID, claim.OutputID) ||
		object.SHA256 != hex.EncodeToString(hash[:]) || object.SizeBytes != int64(len(data)) {
		return nil, "TRANSCRIPTION_UPLOAD_FAILED", true
	}
	logger.Info("transcription stage completed", "stage", "upload")
	return &transcription.Publication{
		Object: object, WordCount: len(doc.Words), DurationMs: doc.AudioDurationMs,
	}, "", false
}

func (h *TranscriptionHandler) Drain(ctx context.Context) error {
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

func (h *TranscriptionHandler) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if _, err := h.store.Recover(ctx, 100); err != nil && ctx.Err() == nil {
			h.logger.Error("transcription recovery failed", "code", "TRANSCRIPTION_DATABASE")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func TranscriptionWorkerConfig(logger *slog.Logger) asynq.Config {
	if logger == nil {
		logger = slog.Default()
	}
	return asynq.Config{
		Concurrency: 1, Queues: map[string]int{transcription.Queue: 1},
		ShutdownTimeout: 30 * time.Second,
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return time.Minute },
		Logger:          processQueueLogger{logger: logger},
	}
}
