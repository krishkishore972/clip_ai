package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/httpserver"
)

// This double never runs in the executable and never contacts AWS.
type uploadServiceStub struct {
	err     error
	calls   int
	op      string
	ctx     context.Context
	id      string
	key     string
	input   any
	session UploadSession
}

func (s *uploadServiceStub) record(ctx context.Context, op, id, key string, input any) {
	s.calls++
	s.ctx, s.op, s.id, s.key, s.input = ctx, op, id, key, input
}

func (s *uploadServiceStub) Create(ctx context.Context, key string, in CreateUploadRequest) (UploadSession, error) {
	s.record(ctx, "create", "", key, in)
	return s.session, s.err
}

func (s *uploadServiceStub) SignParts(ctx context.Context, id string, parts []int) (SignedUploadParts, error) {
	s.record(ctx, "sign", id, "", parts)
	return SignedUploadParts{Parts: []SignedUploadPart{{
		PartNumber: 1, URL: "https://example.invalid/part", Headers: map[string]string{},
		ExpiresAt: time.Date(2026, 11, 12, 12, 15, 0, 0, time.UTC),
	}}}, s.err
}

func (s *uploadServiceStub) Get(ctx context.Context, id string) (UploadSession, error) {
	s.record(ctx, "get", id, "", nil)
	return s.session, s.err
}

func (s *uploadServiceStub) Complete(ctx context.Context, id, key string, in CompleteUploadRequest) (UploadSession, error) {
	s.record(ctx, "complete", id, key, in)
	return s.session, s.err
}

func (s *uploadServiceStub) Abort(ctx context.Context, id, key string) error {
	s.record(ctx, "abort", id, key, nil)
	return s.err
}

func uploadRouter(service MultipartUploadService) http.Handler {
	mux := http.NewServeMux()
	NewMultipartUploadHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	return httpserver.RequestID(mux)
}

const createBody = `{"filename":"sample.mp4","sizeBytes":1024,"contentType":"video/mp4"}`

var uploadEndpoints = []struct {
	method, path, body, operation string
	status                        int
	input                         any
}{
	{http.MethodPost, "/uploads", createBody, "create", http.StatusCreated,
		CreateUploadRequest{Filename: "sample.mp4", SizeBytes: 1024, ContentType: "video/mp4"}},
	{http.MethodPost, "/uploads/session-1/parts/sign", `{"partNumbers":[1,2]}`, "sign", http.StatusOK, []int{1, 2}},
	{http.MethodGet, "/uploads/session-1", "", "get", http.StatusOK, nil},
	{http.MethodPost, "/uploads/session-1/complete", `{"parts":[{"partNumber":1,"eTag":"\"tag\""}]}`, "complete", http.StatusOK,
		CompleteUploadRequest{Parts: []CompleteUploadPart{{PartNumber: 1, ETag: `"tag"`}}}},
	{http.MethodDelete, "/uploads/session-1", "", "abort", http.StatusNoContent, nil},
}

func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "key-1")
	return r
}

func TestMultipartUploadSuccess(t *testing.T) {
	t.Parallel()
	for _, endpoint := range uploadEndpoints {
		t.Run(endpoint.operation, func(t *testing.T) {
			t.Parallel()
			service := &uploadServiceStub{session: UploadSession{
				UploadID: "session-1", Status: "uploaded_unvalidated", PartSizeBytes: 16 << 20, PartCount: 1,
				ExpiresAt: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), UploadedParts: []UploadedPart{},
			}}
			r := request(endpoint.method, endpoint.path, endpoint.body)
			w := httptest.NewRecorder()
			uploadRouter(service).ServeHTTP(w, r)
			if w.Code != endpoint.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
			if service.calls != 1 || service.op != endpoint.operation || !reflect.DeepEqual(service.input, endpoint.input) {
				t.Fatalf("unexpected service invocation: %+v", service)
			}
			if endpoint.operation != "create" && service.id != "session-1" {
				t.Fatalf("session id=%q", service.id)
			}
			if endpoint.operation == "create" || endpoint.operation == "complete" || endpoint.operation == "abort" {
				if service.key != "key-1" {
					t.Fatalf("idempotency key=%q", service.key)
				}
			}
			if httpserver.TraceIDFromContext(service.ctx) != w.Header().Get("X-Request-Id") {
				t.Fatal("request context not forwarded")
			}
			if endpoint.status == http.StatusNoContent {
				if w.Body.Len() != 0 {
					t.Fatal("abort must have no response body")
				}
			} else if endpoint.operation == "sign" {
				var signed SignedUploadParts
				if err := json.Unmarshal(w.Body.Bytes(), &signed); err != nil || len(signed.Parts) != 1 || signed.Parts[0].URL != "https://example.invalid/part" {
					t.Fatalf("unexpected signed response: %s", w.Body.String())
				}
			} else {
				var session UploadSession
				if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || !reflect.DeepEqual(session, service.session) {
					t.Fatalf("unexpected session response: %s", w.Body.String())
				}
			}
		})
	}
}

func assertAPIError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body httpserver.APIError
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != code {
		t.Fatalf("status=%d body=%s; want %d %s", w.Code, w.Body.String(), status, code)
	}
	if body.TraceID == "" || body.TraceID != w.Header().Get("X-Request-Id") {
		t.Fatal("missing or mismatched trace ID")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
}

func TestMultipartUploadUnavailable(t *testing.T) {
	t.Parallel()
	for _, endpoint := range uploadEndpoints {
		t.Run(endpoint.operation, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			uploadRouter(nil).ServeHTTP(w, request(endpoint.method, endpoint.path, endpoint.body))
			assertAPIError(t, w, http.StatusServiceUnavailable, "upload_service_unavailable")
		})
	}
}

func TestMultipartUploadInvalidInput(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"null", "/uploads", "null", 400, "invalid_json"},
		{"empty", "/uploads", "", 400, "invalid_json"},
		{"array", "/uploads", "[]", 400, "invalid_json"},
		{"malformed", "/uploads", "{", 400, "invalid_json"},
		{"trailing", "/uploads", createBody + "]", 400, "invalid_json"},
		{"unknown identity", "/uploads", `{"userId":"attacker"}`, 400, "invalid_json"},
		{"missing metadata", "/uploads", "{}", 400, "invalid_upload"},
		{"path filename", "/uploads", `{"filename":"../sample.mp4","sizeBytes":1,"contentType":"video/mp4"}`, 400, "invalid_upload"},
		{"negative size", "/uploads", `{"filename":"sample.mp4","sizeBytes":-1,"contentType":"video/mp4"}`, 400, "invalid_upload"},
		{"fractional size", "/uploads", `{"filename":"sample.mp4","sizeBytes":1.5,"contentType":"video/mp4"}`, 400, "invalid_json"},
		{"too large", "/uploads", `{"filename":"sample.mp4","sizeBytes":2000000001,"contentType":"video/mp4"}`, 413, "file_too_large"},
		{"unsupported", "/uploads", `{"filename":"sample.txt","sizeBytes":1,"contentType":"text/plain"}`, 400, "unsupported_media"},
		{"mismatched type", "/uploads", `{"filename":"sample.mp4","sizeBytes":1,"contentType":"video/webm"}`, 400, "unsupported_media"},
		{"huge JSON", "/uploads", `{"filename":"` + strings.Repeat("x", 1<<20) + `"}`, 413, "request_too_large"},
		{"invalid session id", "/uploads/bad%20id/parts/sign", `{"partNumbers":[1]}`, 400, "invalid_upload_id"},
		{"empty sign parts", "/uploads/session-1/parts/sign", `{"partNumbers":[]}`, 400, "invalid_parts"},
		{"null sign parts", "/uploads/session-1/parts/sign", `{"partNumbers":null}`, 400, "invalid_parts"},
		{"duplicate sign parts", "/uploads/session-1/parts/sign", `{"partNumbers":[1,1]}`, 400, "invalid_parts"},
		{"zero part", "/uploads/session-1/parts/sign", `{"partNumbers":[0]}`, 400, "invalid_parts"},
		{"out of range part", "/uploads/session-1/parts/sign", `{"partNumbers":[10001]}`, 400, "invalid_parts"},
		{"empty complete", "/uploads/session-1/complete", `{"parts":[]}`, 400, "invalid_parts"},
		{"missing ETag", "/uploads/session-1/complete", `{"parts":[{"partNumber":1}]}`, 400, "invalid_parts"},
		{"duplicate complete parts", "/uploads/session-1/complete", `{"parts":[{"partNumber":1,"eTag":"x"},{"partNumber":1,"eTag":"y"}]}`, 400, "invalid_parts"},
		{"negative complete part", "/uploads/session-1/complete", `{"parts":[{"partNumber":-1,"eTag":"x"}]}`, 400, "invalid_parts"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &uploadServiceStub{}
			w := httptest.NewRecorder()
			uploadRouter(service).ServeHTTP(w, request(http.MethodPost, tt.path, tt.body))
			assertAPIError(t, w, tt.status, tt.code)
			if service.calls != 0 {
				t.Fatal("invalid request reached service")
			}
		})
	}
}

func TestMultipartUploadHeaders(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, key, contentType string
		status                 int
		code                   string
	}{
		{"missing key", "", "application/json", 400, "invalid_idempotency_key"},
		{"invalid key", "bad key", "application/json", 400, "invalid_idempotency_key"},
		{"long key", strings.Repeat("x", 129), "application/json", 400, "invalid_idempotency_key"},
		{"form data", "key-1", "multipart/form-data; boundary=x", 415, "unsupported_content_type"},
		{"missing content type", "key-1", "", 415, "unsupported_content_type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &uploadServiceStub{}
			r := request(http.MethodPost, "/uploads", createBody)
			r.Header.Set("Idempotency-Key", tt.key)
			r.Header.Set("Content-Type", tt.contentType)
			w := httptest.NewRecorder()
			uploadRouter(service).ServeHTTP(w, r)
			assertAPIError(t, w, tt.status, tt.code)
			if service.calls != 0 {
				t.Fatal("invalid request reached service")
			}
		})
	}
}

func TestMultipartUploadServiceErrors(t *testing.T) {
	t.Parallel()
	for _, endpoint := range uploadEndpoints {
		for _, tt := range []struct {
			err    error
			status int
			code   string
		}{
			{ErrUploadNotFound, 404, "upload_not_found"},
			{ErrUploadConflict, 409, "upload_conflict"},
			{ErrUploadUnavailable, 503, "upload_service_unavailable"},
			{ErrUploadTooLarge, 413, "file_too_large"},
			{ErrInvalidUpload, 400, "invalid_upload"},
			{errors.New("secret provider response"), 500, "internal_error"},
		} {
			t.Run(endpoint.operation+"/"+tt.code, func(t *testing.T) {
				t.Parallel()
				service := &uploadServiceStub{err: fmt.Errorf("wrapped: %w", tt.err)}
				w := httptest.NewRecorder()
				uploadRouter(service).ServeHTTP(w, request(endpoint.method, endpoint.path, endpoint.body))
				assertAPIError(t, w, tt.status, tt.code)
				if strings.Contains(w.Body.String(), "secret provider") {
					t.Fatal("internal service error leaked")
				}
			})
		}
	}
}

func TestMultipartUploadCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := &uploadServiceStub{err: context.Canceled}
	r := request(http.MethodGet, "/uploads/session-1", "").WithContext(ctx)
	uploadRouter(service).ServeHTTP(httptest.NewRecorder(), r)
	if service.calls != 1 || !errors.Is(service.ctx.Err(), context.Canceled) {
		t.Fatal("cancellation was not forwarded")
	}
}

func TestMultipartUploadSigningBatchLimit(t *testing.T) {
	t.Parallel()
	parts := make([]int, maxSignBatch+1)
	for i := range parts {
		parts[i] = i + 1
	}
	body, err := json.Marshal(SignUploadPartsRequest{PartNumbers: parts})
	if err != nil {
		t.Fatal(err)
	}
	service := &uploadServiceStub{}
	w := httptest.NewRecorder()
	uploadRouter(service).ServeHTTP(w, request(http.MethodPost, "/uploads/session-1/parts/sign", string(body)))
	assertAPIError(t, w, 400, "invalid_parts")
	if service.calls != 0 {
		t.Fatal("oversized batch reached service")
	}
}

func TestMultipartUploadMutationKeys(t *testing.T) {
	t.Parallel()
	for _, endpoint := range uploadEndpoints {
		if endpoint.operation == "sign" || endpoint.operation == "get" {
			continue
		}
		t.Run(endpoint.operation, func(t *testing.T) {
			t.Parallel()
			service := &uploadServiceStub{}
			r := request(endpoint.method, endpoint.path, endpoint.body)
			r.Header.Del("Idempotency-Key")
			w := httptest.NewRecorder()
			uploadRouter(service).ServeHTTP(w, r)
			assertAPIError(t, w, 400, "invalid_idempotency_key")
			if service.calls != 0 {
				t.Fatal("mutation without a key reached service")
			}
		})
	}
}

func TestMultipartUploadMetadataBoundary(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct{ filename, contentType string }{
		{"sample.MP4", "video/mp4"}, {"sample.mov", "video/quicktime"}, {"sample.webm", "video/webm"},
	} {
		t.Run(pair.filename, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(CreateUploadRequest{Filename: pair.filename, ContentType: pair.contentType, SizeBytes: maxUploadBytes})
			if err != nil {
				t.Fatal(err)
			}
			service := &uploadServiceStub{}
			w := httptest.NewRecorder()
			uploadRouter(service).ServeHTTP(w, request(http.MethodPost, "/uploads", string(body)))
			if w.Code != http.StatusCreated || service.calls != 1 {
				t.Fatalf("boundary rejected: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
