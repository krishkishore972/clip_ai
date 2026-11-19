// Command dispatcher forwards durable outbox events to Redis/Asynq.
// It does not run task handlers or apply migrations.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/job"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadDispatcher()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if !cfg.Enabled {
		logger.Info("outbox dispatcher disabled; enable only after registering the real worker")
		return nil
	}
	redisConfig, err := config.LoadRedis()
	if err != nil {
		return err
	}
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		return errors.New("dispatcher database connection failed; check DATABASE_URL and PostgreSQL availability")
	}
	defer database.Close()
	client := job.NewClient(redisConfig)
	defer client.Close()
	dispatcher, err := job.NewDispatcher(database, client, job.DispatcherOptions{
		PollInterval:       cfg.PollInterval,
		TransactionTimeout: cfg.TransactionTimeout,
		EnqueueTimeout:     cfg.EnqueueTimeout,
		RetryBase:          cfg.RetryBase,
		RetryMax:           cfg.RetryMax,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("outbox dispatcher started")
	dispatcher.Run(ctx, logger)
	logger.Info("outbox dispatcher stopped")
	return nil
}
