package config

import (
	"errors"
	"log/slog"
	"strconv"
	"time"
)

// Dispatcher is loaded independently of HTTP, Clerk and S3 configuration.
// DatabaseURL is secret and must never be logged.
type Dispatcher struct {
	Enabled            bool
	DatabaseURL        string
	LogLevel           slog.Level
	PollInterval       time.Duration
	TransactionTimeout time.Duration
	EnqueueTimeout     time.Duration
	RetryBase          time.Duration
	RetryMax           time.Duration
}

func LoadDispatcher() (Dispatcher, error) {
	var cfg Dispatcher
	raw, err := required("OUTBOX_DISPATCH_ENABLED")
	if err != nil {
		return cfg, err
	}
	cfg.Enabled, err = strconv.ParseBool(raw)
	if err != nil {
		return cfg, errors.New("config: OUTBOX_DISPATCH_ENABLED must be a boolean")
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	var errs []error
	if cfg.DatabaseURL, err = required("DATABASE_URL"); err != nil {
		errs = append(errs, err)
	}
	if cfg.LogLevel, err = requiredLevel("API_LOG_LEVEL"); err != nil {
		errs = append(errs, err)
	}
	for _, field := range []struct {
		key string
		dst *time.Duration
	}{
		{"OUTBOX_POLL_INTERVAL", &cfg.PollInterval},
		{"OUTBOX_TRANSACTION_TIMEOUT", &cfg.TransactionTimeout},
		{"OUTBOX_ENQUEUE_TIMEOUT", &cfg.EnqueueTimeout},
		{"OUTBOX_RETRY_BASE", &cfg.RetryBase},
		{"OUTBOX_RETRY_MAX", &cfg.RetryMax},
	} {
		if *field.dst, err = requiredDuration(field.key); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) != 0 {
		return Dispatcher{}, errors.Join(errs...)
	}
	if cfg.PollInterval < time.Millisecond ||
		cfg.EnqueueTimeout < time.Millisecond ||
		cfg.RetryBase < time.Millisecond ||
		cfg.RetryMax < cfg.RetryBase ||
		cfg.TransactionTimeout <= cfg.EnqueueTimeout {
		return Dispatcher{}, errors.New("config: outbox durations must be at least 1ms, transaction timeout must exceed enqueue timeout, and retry max must not be less than retry base")
	}
	return cfg, nil
}
