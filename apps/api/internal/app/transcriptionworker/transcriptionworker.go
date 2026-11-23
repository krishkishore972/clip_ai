// Package transcriptionworker composes the opt-in S3-to-transcript worker.
package transcriptionworker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func Run() error {
	cfg, err := config.LoadTranscriptionWorker()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if !cfg.Enabled {
		logger.Info("transcription worker disabled")
		return nil
	}
	redis, err := config.LoadRedis()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		return errors.New("transcription worker database unavailable")
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := transcription.CheckFileSchema(startup, pool); err != nil {
		return errors.New("transcription worker requires approved migration 00008")
	}
	downloader, err := storage.NewDownloader(startup, cfg.Region, cfg.Bucket)
	if err != nil {
		return errors.New("transcription worker downloader unavailable")
	}
	uploader, err := storage.NewDerivedUploader(startup, config.S3{Region: cfg.Region, Bucket: cfg.Bucket})
	if err != nil {
		return errors.New("transcription worker uploader unavailable")
	}
	provider, err := transcription.NewClient(cfg.APIKey, nil)
	if err != nil {
		return err
	}
	store := transcription.SQLStore{DB: pool}
	handler, err := job.NewTranscriptionHandler(store, downloader, uploader, provider, cfg.TempDir, cfg.Bucket, logger)
	if err != nil {
		return err
	}
	if _, err := store.Recover(startup, 100); err != nil {
		return errors.New("transcription worker startup recovery unavailable")
	}
	cancel()
	server := asynq.NewServer(redis.Options, job.TranscriptionWorkerConfig(logger))
	mux := asynq.NewServeMux()
	mux.HandleFunc(transcription.TaskType, handler.ProcessTask)
	if err := server.Start(mux); err != nil {
		return errors.New("transcription worker queue unavailable")
	}
	recovery, cancelRecovery := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); handler.RunRecovery(recovery) }()
	logger.Info("transcription worker started")
	<-ctx.Done()
	server.Shutdown()
	drain, cancelDrain := context.WithTimeout(context.Background(), 15*time.Second)
	err = handler.Drain(drain)
	cancelDrain()
	cancelRecovery()
	<-done
	if err != nil {
		return errors.New("transcription worker drain expired")
	}
	logger.Info("transcription worker stopped")
	return nil
}
