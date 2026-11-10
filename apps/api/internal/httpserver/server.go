// Package httpserver holds HTTP-only concerns: JSON responses, request
// decoding, structured errors, middleware, and server construction.
//
// It contains no business logic and knows nothing about projects, media, or
// jobs. Handlers in this package translate between HTTP and domain services.
package httpserver

import (
	"net/http"

	"github.com/krishkishore972/clip_ai/internal/config"
)

// NewServer returns an http.Server with the timeouts and address from cfg.
// Callers own Serve/Shutdown; this function only constructs.
func NewServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}
