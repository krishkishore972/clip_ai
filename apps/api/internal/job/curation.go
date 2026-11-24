package job

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

const CurationDownloadTimeout = 30 * time.Second

type CurationStore interface {
	Claim(context.Context, string, string) (curation.Claim, error)
	Finish(context.Context, curation.Claim, *curation.Publication, string, bool) error
	Recover(context.Context, int) (int, error)
}

type Curator interface {
	Curate(context.Context, curation.Request) ([]curation.Proposal, error)
}

type CurationHandler struct {
	store        CurationStore
	downloader   VideoDownloader
	curator      Curator
	root, bucket string
	logger       *slog.Logger
	lifetime     context.Context
	cancel       context.CancelFunc
	serial       chan struct{}
	mu           sync.Mutex
	closed       bool
	active       sync.WaitGroup
}

func NewCurationHandler(store CurationStore, downloader VideoDownloader, curator Curator, root, bucket string, logger *slog.Logger) (*CurationHandler, error) {
	if store == nil || downloader == nil || curator == nil || bucket == "" || logger == nil {
		return nil, errors.New("curation: worker dependencies are required")
	}
	privateRoot, err := prepareProcessRoot(root)
	if err != nil {
		return nil, errors.New("curation: private temporary root unavailable")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &CurationHandler{
		store: store, downloader: downloader, curator: curator,
		root: privateRoot, bucket: bucket, logger: logger, lifetime: lifetime, cancel: cancel,
		serial: make(chan struct{}, 1),
	}, nil
}

var curationAssetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (h *CurationHandler) ProcessTask(parent context.Context, task *asynq.Task) error {
	if task == nil || task.Type() != curation.TaskType || len(task.Payload()) > 1024 {
		return asynq.SkipRetry
	}
	var payload struct {
		AssetID string `json:"assetId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(task.Payload()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || !curationAssetPattern.MatchString(payload.AssetID) {
		return asynq.SkipRetry
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return asynq.SkipRetry
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("curation: worker stopped")
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	ctx, cancel := context.WithTimeout(parent, curation.TaskTimeout)
	defer cancel()
	stop := context.AfterFunc(h.lifetime, cancel)
	defer stop()
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-ctx.Done():
		return errors.New("curation: task interrupted")
	}
	if ctx.Err() != nil || h.lifetime.Err() != nil {
		return errors.New("curation: task interrupted")
	}
	logger := h.logger.With("asset_id", payload.AssetID)
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return errors.New("curation: claim unavailable")
	}
	claim, err := h.store.Claim(ctx, payload.AssetID, hex.EncodeToString(token[:]))
	if errors.Is(err, curation.ErrBusy) || err == nil && claim.AssetID == "" {
		return nil
	}
	if err != nil {
		logger.Error("curation claim failed", "code", "CURATION_DATABASE")
		return errors.New("curation: claim failed")
	}
	deadline, _ := ctx.Deadline()
	work, stopWork := context.WithDeadline(ctx, deadline.Add(-5*time.Second))
	pub, code, retry := h.process(work, claim, logger)
	stopWork()
	if ctx.Err() != nil || h.lifetime.Err() != nil {
		pub, code, retry = nil, "CURATION_INTERRUPTED", true
	}
	// Keep the serial slot until processing has actually returned and this
	// bounded finish is done, including on caller cancellation.
	finish, stopFinish := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer stopFinish()
	err = h.store.Finish(finish, claim, pub, code, retry)
	if errors.Is(err, curation.ErrStale) {
		return nil
	}
	if err != nil {
		logger.Error("curation publication failed", "code", "CURATION_DATABASE")
		return errors.New("curation: publication failed")
	}
	if pub == nil {
		logger.Warn("curation attempt failed", "attempt", claim.Attempts, "code", code)
	} else {
		// Finish may instead commit a source-changed failure after its own
		// final eligibility check; do not claim readiness from a proposal alone.
		logger.Info("curation attempt committed", "attempt", claim.Attempts)
	}
	// Committed outcomes include durable retries. Do not also retry in Asynq.
	return nil
}

func (h *CurationHandler) process(ctx context.Context, c curation.Claim, logger *slog.Logger) (*curation.Publication, string, bool) {
	if c.TranscriptBucket != h.bucket || !c.Valid() {
		return nil, "CURATION_SOURCE_CHANGED", false
	}
	dir, err := os.MkdirTemp(h.root, "curate-")
	if err != nil {
		return nil, "CURATION_STORAGE_FAILED", true
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			logger.Warn("curation temporary cleanup failed", "code", "TEMP_CLEANUP_FAILED")
		}
	}()
	path := filepath.Join(dir, "transcript.json")
	stage, cancel := context.WithTimeout(ctx, CurationDownloadTimeout)
	err = h.downloader.Download(stage, c.TranscriptKey, path, c.TranscriptSizeBytes)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrDownloadSizeMismatch) || errors.Is(err, storage.ErrObjectNotFound) {
			return nil, "CURATION_SOURCE_CHANGED", false
		}
		return nil, "CURATION_DOWNLOAD_FAILED", true
	}
	data, err := readCurationFile(path, c.TranscriptSizeBytes)
	if err != nil {
		return nil, "CURATION_STORAGE_FAILED", true
	}
	// This verifies hash, size, identity and all word timing/text before any
	// private content is sent to the service, including the empty short circuit.
	empty, err := curation.ValidateResult(c, data, nil)
	if err != nil {
		return nil, "CURATION_SOURCE_CHANGED", false
	}
	if c.TranscriptWordCount == 0 {
		return empty, "", false
	}
	var doc transcription.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, "CURATION_SOURCE_CHANGED", false
	}
	projected, err := doc.WordsForCuration()
	if err != nil {
		return nil, "CURATION_SOURCE_CHANGED", false
	}
	words := make([]curation.Word, len(projected))
	for i, w := range projected {
		words[i] = curation.Word{Index: w.Index, Text: w.Text, StartMs: w.StartMs, EndMs: w.EndMs}
	}
	if ctx.Err() != nil {
		return nil, "CURATION_TIMEOUT", true
	}
	proposals, err := h.curator.Curate(ctx, curation.Request{
		TranscriptHash: "sha256:" + c.TranscriptSHA256, Words: words,
		Constraints: curation.Constraints{
			CandidateCount: c.CandidateCount, MinDurationMs: c.MinDurationMs, MaxDurationMs: c.MaxDurationMs,
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, curation.ErrServiceAuth):
			return nil, "CURATION_PROVIDER_AUTH", false
		case errors.Is(err, curation.ErrServiceRejected):
			return nil, "CURATION_PROVIDER_REJECTED", false
		case errors.Is(err, curation.ErrInputTooLarge):
			return nil, "CURATION_INPUT_TOO_LARGE", false
		case errors.Is(err, curation.ErrResult):
			return nil, "CURATION_INVALID_OUTPUT", false
		case errors.Is(err, context.DeadlineExceeded):
			return nil, "CURATION_TIMEOUT", true
		case errors.Is(err, context.Canceled):
			return nil, "CURATION_INTERRUPTED", true
		default:
			return nil, "CURATION_PROVIDER_FAILED", true
		}
	}
	pub, err := curation.ValidateResult(c, data, proposals)
	if err != nil {
		return nil, "CURATION_INVALID_OUTPUT", false
	}
	return pub, "", false
}

func readCurationFile(path string, size int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return nil, curation.ErrResult
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, curation.ErrResult
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, curation.ErrResult
	}
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil || int64(len(data)) != size {
		return nil, curation.ErrResult
	}
	return data, nil
}

func (h *CurationHandler) Drain(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
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

func (h *CurationHandler) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := h.store.Recover(ctx, 100); err != nil && ctx.Err() == nil {
			h.logger.Error("curation recovery failed", "code", "CURATION_DATABASE")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func CurationWorkerConfig(logger *slog.Logger) asynq.Config {
	if logger == nil {
		logger = slog.Default()
	}
	return asynq.Config{
		Concurrency: 1, Queues: map[string]int{curation.Queue: 1},
		ShutdownTimeout: 15 * time.Second,
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return curation.RetryDelay },
		Logger:          processQueueLogger{logger: logger},
	}
}
