package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// Middleware wraps a handler. Chain applies them in order, outermost first.
type Middleware func(http.Handler) http.Handler

// Chain returns h wrapped by mw, where mw[0] is the outermost layer.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type ctxKey int

const traceKey ctxKey = iota

// TraceIDFromContext returns the request id installed by RequestID, or "".
func TraceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceKey).(string)
	return id
}

// RequestID honours an inbound X-Request-Id or generates one, echoes it on the
// response, and stores it in the context for logging and error bodies.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}

		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceKey, id)))
	})
}

// recorder captures the status and size of a response so LogRequests can
// report them. It does not implement Flusher or Hijacker; add passthroughs
// before serving streaming or WebSocket endpoints through this middleware.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	// Informational responses may precede a final response. A protocol switch
	// is different: after 101, an HTTP error response is no longer possible.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// LogRequests emits one structured line per request with the trace id.
func LogRequests(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}

			defer func() {
				if rec.status == 0 {
					rec.status = http.StatusOK
				}

				log.InfoContext(r.Context(), "http_request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rec.status,
					"bytes", rec.bytes,
					"duration_ms", time.Since(start).Milliseconds(),
					"trace_id", TraceIDFromContext(r.Context()),
				)
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// Recover turns a handler panic into a logged 500 with an APIError body.
//
// Once a final response has started, log the panic and abort the response via
// net/http rather than append an error body to an already committed response.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Track commitment even when Recover is installed without LogRequests.
			rw, ok := w.(*recorder)
			if !ok {
				rw = &recorder{ResponseWriter: w}
			}
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.ErrorContext(r.Context(), "panic recovered",
						"panic", rec,
						"stack", string(debug.Stack()),
						"trace_id", TraceIDFromContext(r.Context()),
					)

					if rw.status != 0 {
						panic(http.ErrAbortHandler)
					}
					rw.Header().Del("Content-Length")
					if err := WriteError(rw, r, http.StatusInternalServerError,
						"internal_error", "internal server error", nil); err != nil {
						log.ErrorContext(r.Context(), "write recovery response", "err", err,
							"trace_id", TraceIDFromContext(r.Context()))
					}
				}
			}()

			next.ServeHTTP(rw, r)
		})
	}
}
