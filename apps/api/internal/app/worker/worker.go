// Package worker implements the worker application independently of its CLI.
package worker

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

// Run executes the application using its existing opt-in and lifecycle policy.
func Run() error {
	cfg, err := config.LoadValidationWorker()

	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if !cfg.Enabled {
		logger.Info("validation worker disabled")
		return nil
	}

	redisConfig, err := config.LoadRedis()
	if err != nil {
		return err
	}

	if _, err := exec.LookPath("ffprobe"); err != nil {
		return errors.New("validation worker requires ffprobe on PATH")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		return errors.New("validation worker database connection failed")
	}

	defer database.Close()

	startupCtx, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStartup()

	if err := checkValidationSchema(startupCtx, database); err != nil {
		return err
	}

	downloader, err := storage.NewDownloader(startupCtx, cfg.Region, cfg.Bucket)
	if err != nil {
		return errors.New("validation worker storage initialization failed")
	}

	store := job.SQLValidationStore{DB: database}
	handler, err := job.NewValidationHandler(store, downloader, job.ValidationOptions{
		Bucket:          cfg.Bucket,
		TempDir:         cfg.TempDir,
		DownloadTimeout: 15 * time.Minute,
		ProbeTimeout:    time.Minute,
		TaskTimeout:     20 * time.Minute,
	}, logger)

	if err != nil {
		return errors.New("validation worker handler initialization failed")
	}

	// One bounded batch repairs stale ownership before any new consumption.
	// Periodic recovery handles remaining rows; startup never loops unboundedly.
	if _, err := store.Recover(startupCtx, 100); err != nil {
		return errors.New("validation worker startup recovery failed")
	}
	cancelStartup()
	if ctx.Err() != nil {
		return nil
	}

	inspector := job.NewValidationInspector(redisConfig)
	defer inspector.Close()

	handler.SetArchiveInspector(inspector)
	server := asynq.NewServer(redisConfig.Options, job.ValidationWorkerConfig(logger))
	mux := asynq.NewServeMux()

	mux.HandleFunc(job.ValidationTaskType, handler.ProcessTask)
	if err := server.Start(mux); err != nil {
		return errors.New("validation worker queue startup failed")
	}

	// Do not derive task/recovery contexts from the signal context: a signal
	// starts Asynq's drain, rather than canceling active work immediately.
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	recoveryDone := make(chan struct{})

	go func() {
		defer close(recoveryDone)
		handler.RunRecovery(recoveryCtx)
	}()

	logger.Info("validation worker started")
	<-ctx.Done()
	server.Shutdown()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	if err := handler.Drain(drainCtx); err != nil {
		logger.Error("validation handler drain expired", "code", "WORKER_DRAIN_TIMEOUT")
	}

	cancelDrain()
	cancelRecovery()
	<-recoveryDone
	logger.Info("validation worker stopped")
	return nil

}

func checkValidationSchema(ctx context.Context, database *sql.DB) error {
	const query = `
		SELECT
			validation_attempts,
			validation_claim_token,
			validation_lease_until,
			probed_metadata
		FROM media_assets
		LIMIT 0
	`
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return errors.New("validation worker schema unavailable; apply approved migrations separately")
	}

	defer rows.Close()
	if err := rows.Err(); err != nil {
		return errors.New("validation worker schema check failed")
	}

	return nil
}
