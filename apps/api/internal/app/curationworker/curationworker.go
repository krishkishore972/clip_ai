// Package curationworker composes the opt-in transcript-to-proposals consumer.
package curationworker

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
	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

func Run() error {
	cfg, err := config.LoadCurationWorker()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if !cfg.Enabled {
		logger.Info("curation worker disabled")
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
		return errors.New("curation worker database unavailable")
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := transcription.CheckFileSchema(startup, pool); err != nil {
		return errors.New("curation worker requires approved transcript schema")
	}
	if err := curation.CheckSchema(startup, pool); err != nil {
		return errors.New("curation worker requires approved migration 00009")
	}
	downloader, err := storage.NewDownloader(startup, cfg.Region, cfg.Bucket)
	if err != nil {
		return errors.New("curation worker downloader unavailable")
	}
	client, err := curation.NewClient(curation.ClientOptions{URL: cfg.ServiceURL, Token: cfg.ServiceToken}, nil)
	if err != nil {
		return err
	}
	store := curation.SQLStore{DB: pool}
	handler, err := job.NewCurationHandler(store, downloader, client, cfg.TempDir, cfg.Bucket, logger)
	if err != nil {
		return err
	}
	workerConfig := job.CurationWorkerConfig(logger)
	workerConfig.BaseContext = func() context.Context { return ctx }
	server := asynq.NewServer(redis.Options, workerConfig)
	mux := asynq.NewServeMux()
	mux.HandleFunc(curation.TaskType, handler.ProcessTask)
	if err := server.Start(mux); err != nil {
		return errors.New("curation worker queue unavailable")
	}
	recovery, cancelRecovery := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); handler.RunRecovery(recovery) }()
	logger.Info("curation worker started")
	<-ctx.Done()
	cancelRecovery()
	server.Stop()
	drain, cancelDrain := context.WithTimeout(context.Background(), 15*time.Second)
	drainErr := handler.Drain(drain)
	cancelDrain()
	server.Shutdown()
	<-done
	if drainErr != nil {
		return errors.New("curation worker drain expired")
	}
	logger.Info("curation worker stopped")
	return nil
}
