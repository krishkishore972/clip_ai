package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestChainOrder(t *testing.T) {
	t.Parallel()

	var order []string
	tag := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name+"-before")
				next.ServeHTTP(w, r)
				order = append(order, name+"-after")
			})
		}
	}

	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}), tag("outer"), tag("inner"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"outer-before", "inner-before", "handler", "inner-after", "outer-after"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestRequestIDGenerates(t *testing.T) {
	t.Parallel()

	var inContext string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inContext = TraceIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if inContext == "" {
		t.Fatal("trace id missing from context")
	}
	if got := rec.Header().Get("X-Request-Id"); got != inContext {
		t.Errorf("response header %q != context %q", got, inContext)
	}
	if len(inContext) != 16 {
		t.Errorf("generated id %q has length %d, want 16", inContext, len(inContext))
	}
}

func TestRequestIDHonoursInbound(t *testing.T) {
	t.Parallel()

	var inContext string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inContext = TraceIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "abc123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if inContext != "abc123" {
		t.Errorf("context id = %q, want abc123", inContext)
	}
	if got := rec.Header().Get("X-Request-Id"); got != "abc123" {
		t.Errorf("response header = %q, want abc123", got)
	}
}

func TestRecoverReturns500(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}), RequestID, Recover(log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var body APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Code != "internal_error" {
		t.Errorf("code = %q, want internal_error", body.Code)
	}
	if body.TraceID == "" {
		t.Error("traceId missing from error body")
	}
}

func TestLogRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    http.Handler
		wantStatus int
	}{
		{
			name: "explicit status",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("ok"))
			}),
			wantStatus: http.StatusCreated,
		},
		{
			name: "implicit 200",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("ok"))
			}),
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))

			h := Chain(tt.handler, RequestID, LogRequests(log))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

			out := buf.String()
			if !strings.Contains(out, "http_request") {
				t.Errorf("log line missing http_request:\n%s", out)
			}
			if want := "status=" + strconv.Itoa(tt.wantStatus); !strings.Contains(out, want) {
				t.Errorf("log line missing %s:\n%s", want, out)
			}
			if !strings.Contains(out, "trace_id=") {
				t.Errorf("log line missing trace_id:\n%s", out)
			}
		})
	}
}

func TestWriteErrorIncludesTraceID(t *testing.T) {
	t.Parallel()

	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusNotFound, "project_not_found", "project not found", nil)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects/x", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	var body APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Code != "project_not_found" {
		t.Errorf("code = %q, want project_not_found", body.Code)
	}
	if body.TraceID != rec.Header().Get("X-Request-Id") {
		t.Errorf("traceId %q does not match header %q", body.TraceID, rec.Header().Get("X-Request-Id"))
	}
}
