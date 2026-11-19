// Command server is the HTTP entrypoint for the API.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/krishkishore972/clip_ai/internal/app/server"
	"github.com/krishkishore972/clip_ai/internal/config"
)

// version is stamped at build time via -ldflags "-X main.version=<git describe>".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := server.Serve(cfg, logger, version); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}
