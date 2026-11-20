// Package mediaworker composes the opt-in, combined validation and preparation process.
package mediaworker

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

func Run() error {
	cfg, err := config.LoadPreparationWorker()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if !cfg.Enabled {
		logger.Info("media process worker disabled")
		return nil
	}
	redisConfig, err := config.LoadRedis()
	if err != nil {
		return err
	}
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errors.New("media process worker requires ffmpeg and ffprobe on PATH")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		return errors.New("media process worker database connection failed")
	}
	defer database.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := CheckSchema(startup, database); err != nil {
		return err
	}
	downloader, err := storage.NewDownloader(startup, cfg.Region, cfg.Bucket)
	if err != nil {
		return errors.New("media process worker storage initialization failed")
	}
	uploader, err := storage.NewDerivedUploader(startup, config.S3{Region: cfg.Region, Bucket: cfg.Bucket})
	if err != nil {
		return errors.New("preparation worker storage initialization failed")
	}
	store := job.SQLProcessStore{DB: database}
	handler, err := job.NewProcessHandler(store, downloader, uploader, job.ProcessOptions{Bucket: cfg.Bucket, TempDir: cfg.TempDir, DownloadTimeout: 15 * time.Minute, EncodeTimeout: 60 * time.Minute, UploadTimeout: 10 * time.Minute, TaskTimeout: 90 * time.Minute}, logger)
	if err != nil {
		return errors.New("media process worker handler initialization failed")
	}
	if err := CheckTemporaryCapacity(cfg.TempDir, os.Getenv("VALIDATION_TEMP_DIR")); err != nil {
		return err
	}
	if _, err := store.Recover(startup, 100); err != nil {
		return errors.New("media process worker startup recovery failed")
	}
	cancel()
	if ctx.Err() != nil {
		return nil
	}
	server := asynq.NewServer(redisConfig.Options, job.ProcessWorkerConfig(logger))
	mux := asynq.NewServeMux()
	mux.HandleFunc(job.ProcessTaskType, handler.ProcessTask)
	// Initial completed uploads retain the existing validation event name; the
	// combined worker consumes it and performs validation plus preparation.
	mux.HandleFunc(job.ValidationTaskType, handler.ProcessTask)
	// Accept preparation events created by older clients while migration 00006
	// is still in the development rollout window.
	mux.HandleFunc(preparation.TaskType, handler.ProcessTask)
	if err := server.Start(mux); err != nil {
		return errors.New("media process worker queue startup failed")
	}
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); handler.RunRecovery(recoveryCtx) }()
	logger.Info("media process worker started")
	<-ctx.Done()
	server.Shutdown()
	drain, cancelDrain := context.WithTimeout(context.Background(), 15*time.Second)
	drainErr := handler.Drain(drain)
	cancelDrain()
	cancelRecovery()
	<-done
	if drainErr != nil {
		return errors.New("media process worker drain expired")
	}
	logger.Info("media process worker stopped")
	return nil
}

// CheckSchema reads schema only; migrations are always applied separately.
func CheckSchema(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("media process worker schema unavailable; database is required")
	}
	rows, err := database.QueryContext(ctx, `
		SELECT validation_attempts, validation_claim_token, validation_lease_until, probed_metadata
		FROM media_assets
		LIMIT 0
	`)
	if err != nil {
		return errors.New("media process worker schema unavailable; apply validation migrations separately")
	}
	rows.Close()
	if err := preparation.CheckSchema(ctx, database); err != nil {
		return errors.New("media process worker preparation schema unavailable; apply approved migration 00006 separately")
	}
	return nil
}

// CheckTemporaryCapacity is a CLI preflight, deliberately not imposed on unit fixtures.
func CheckTemporaryCapacity(root, validationRoot string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("media process worker temporary root unavailable")
	}
	if validationRoot != "" {
		validation, err := filepath.EvalSymlinks(validationRoot)
		if err != nil {
			validation = filepath.Clean(validationRoot)
		}
		if resolved == validation || isWithin(resolved, validation) || isWithin(validation, resolved) {
			return errors.New("media process worker temporary root must be separate from validation")
		}
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(resolved, &stats); err != nil || stats.Bsize <= 0 || stats.Bavail < uint64((8*1024*1024*1024+stats.Bsize-1)/stats.Bsize) {
		return errors.New("media process worker requires at least 8 GiB free temporary space")
	}
	return nil
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel != "." && !(len(rel) >= 3 && rel[:3] == "../")
}
