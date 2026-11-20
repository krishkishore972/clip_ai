package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/handler"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

type preparationHTTPStore struct {
	status         preparation.Status
	err            error
	reads, ensures int
	owner, id      string
}

func (s *preparationHTTPStore) GetStatus(_ context.Context, owner, id string) (preparation.Status, error) {
	s.reads++
	s.owner, s.id = owner, id
	return s.status, s.err
}
func (s *preparationHTTPStore) Ensure(_ context.Context, owner, id string) (preparation.Status, error) {
	s.ensures++
	s.owner, s.id = owner, id
	return s.status, s.err
}

func preparationHTTPFixture() *preparationHTTPStore {
	now := time.Now().UTC()
	return &preparationHTTPStore{
		status: preparation.Status{AssetID: "asset", Status: "ready", Attempts: 1, UpdatedAt: now, PreparedAt: &now},
	}
}
func preparationHTTPRequest(t *testing.T, store *preparationHTTPStore, enabled, authenticated bool, method, path, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	mux := http.NewServeMux()
	handler.NewMediaPreparationHandler(store, enabled, slog.New(slog.NewJSONHandler(&logs, nil))).Register(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "owner-from-session"}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w, logs.String()
}

func TestPreparationHTTPAuthenticationAndTrustedOwner(t *testing.T) {
	for _, op := range []struct{ method, path string }{{"GET", "/media/asset/preparation"}, {"POST", "/media/asset/preparation"}} {
		t.Run(op.method+op.path, func(t *testing.T) {
			s := preparationHTTPFixture()
			w, _ := preparationHTTPRequest(t, s, true, false, op.method, op.path, "")
			if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("WWW-Authenticate") != "Bearer" || s.reads+s.ensures != 0 {
				t.Fatalf("unauthenticated request reached dependencies: %d %s", w.Code, w.Body.String())
			}
			w, _ = preparationHTTPRequest(t, s, true, true, op.method, op.path+"?userId=attacker", "")
			if w.Code != 200 && w.Code != 202 {
				t.Fatalf("authenticated response: %d %s", w.Code, w.Body.String())
			}
			if s.owner != "owner-from-session" || s.id != "asset" {
				t.Fatalf("untrusted owner: %q %q", s.owner, s.id)
			}
		})
	}
}

func TestPreparationHTTPStatusIsReadOnlyAndSanitized(t *testing.T) {
	s := preparationHTTPFixture()
	unknown := "postgres://secret-user:secret-password@provider"
	s.status.Status = "failed"
	s.status.FailureCode = &unknown
	s.status.PreparedAt = nil
	w, logs := preparationHTTPRequest(t, s, true, true, "GET", "/media/asset/preparation", "")
	if w.Code != 200 || s.reads != 1 || s.ensures != 0 {
		t.Fatalf("status caused work: %d", w.Code)
	}
	var result preparation.Status
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.FailureCode == nil || *result.FailureCode != "PREPARATION_FAILED" || strings.Contains(w.Body.String()+logs, "secret-password") || strings.Contains(w.Body.String(), "dev-uploads") {
		t.Fatalf("unsafe status: %s", w.Body.String())
	}
}

func TestPreparationHTTPFailureBoundaries(t *testing.T) {
	for _, op := range []struct{ method, path string }{{"GET", "/media/asset/preparation"}, {"POST", "/media/asset/preparation"}} {
		for _, failure := range []struct {
			err  error
			code int
		}{{preparation.ErrNotFound, 404}, {preparation.ErrNotReady, 409}, {errors.New("raw dependency secret"), 503}} {
			s := preparationHTTPFixture()
			s.err = failure.err
			w, logs := preparationHTTPRequest(t, s, true, true, op.method, op.path, "")
			if w.Code != failure.code || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String()+logs, "raw dependency") {
				t.Fatalf("unsafe failure %s: %d %s", op.path, w.Code, w.Body.String())
			}
		}
	}
	for _, method := range []string{"GET", "POST"} {
		s := preparationHTTPFixture()
		w, _ := preparationHTTPRequest(t, s, false, true, method, "/media/asset/preparation", "")
		if w.Code != 503 || s.reads+s.ensures != 0 {
			t.Fatalf("disabled feature did work: %d", w.Code)
		}
	}
}

func TestPreparationHTTPEnsureAcceptsNoOverrides(t *testing.T) {
	for _, body := range []string{`{"userId":"attacker"}`, `{"retry":true}`, `{"url":"https://attacker"}`, `{}`, " "} {
		s := preparationHTTPFixture()
		w, _ := preparationHTTPRequest(t, s, true, true, "POST", "/media/asset/preparation", body)
		if w.Code != 400 || s.ensures != 0 {
			t.Fatalf("accepted override %q: %d", body, w.Code)
		}
	}
}

// Ensure the dependencies can remain unavailable without making startup routes
// public or requiring AWS in handler-only tests.
func TestPreparationHTTPUnavailableDependencies(t *testing.T) {
	mux := http.NewServeMux()
	handler.NewMediaPreparationHandler(nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	r := httptest.NewRequest("GET", "/media/asset/preparation", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "owner"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing dependency: %d", w.Code)
	}
}
