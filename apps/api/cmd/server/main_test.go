package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadRoutesDevelopmentOnly(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"development", "test", "production", "staging", ""} {
		t.Run(env, func(t *testing.T) {
			t.Parallel()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			h := newHandler(logger, env, nil)
			r := httptest.NewRequest(http.MethodPost, "/uploads", strings.NewReader(
				`{"filename":"sample.mp4","sizeBytes":1024,"contentType":"video/mp4"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "dev-1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusNotFound
			if env == "development" {
				want = http.StatusServiceUnavailable
				if !strings.Contains(w.Body.String(), `"code":"upload_service_unavailable"`) {
					t.Fatalf("unexpected body: %s", w.Body.String())
				}
			}
			if w.Code != want {
				t.Fatalf("status=%d, want %d", w.Code, want)
			}
			if w.Header().Get("X-Request-Id") == "" {
				t.Fatal("missing middleware request ID")
			}
		})
	}
}

func TestHealthRouteUnchanged(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := httptest.NewRecorder()
	newHandler(logger, "production", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}
