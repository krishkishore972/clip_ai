package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/media"
)

type statusReaderFunc func(context.Context, string) (media.AssetStatus, error)

func (f statusReaderFunc) GetStatus(ctx context.Context, id string) (media.AssetStatus, error) {
	return f(ctx, id)
}

func TestMediaStatusAllStates(t *testing.T) {
	t.Parallel()
	for _, state := range []string{media.StatusUploadedUnvalidated, media.StatusValidating, media.StatusReady, media.StatusRejected, media.StatusValidationFailed} {
		t.Run(state, func(t *testing.T) {
			now := time.Date(2026, 11, 19, 12, 30, 0, 0, time.FixedZone("fixture", 3600))
			code := "DURATION_LIMIT"
			reader := statusReaderFunc(func(ctx context.Context, id string) (media.AssetStatus, error) {
				if p, ok := auth.FromContext(ctx); !ok || p.UserID != "server-owner" || id != "asset" {
					t.Fatal("lost server principal or path ID")
				}
				out := media.AssetStatus{AssetID: id, Status: state, Attempts: 2, UpdatedAt: now}
				if state == media.StatusRejected || state == media.StatusValidationFailed {
					out.FailureCode = &code
				}
				if state == media.StatusReady {
					out.ValidatedAt, out.FailureCode = &now, &code // ready always clears it
				}
				return out, nil
			})
			mux := http.NewServeMux()
			NewMediaStatusHandler(reader, nil).Register(mux)
			r := httptest.NewRequest("GET", "/media/asset/status", nil)
			r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "server-owner"}))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 6 {
				t.Fatalf("non-status fields exposed: %v", body)
			}
			for _, key := range []string{"assetId", "status", "attempts", "failureCode", "updatedAt", "validatedAt"} {
				if _, ok := body[key]; !ok {
					t.Fatalf("missing required field %s", key)
				}
			}
			if body["updatedAt"] != "2026-11-19T11:30:00Z" || body["status"] != state {
				t.Fatal(body)
			}
			if state == media.StatusReady {
				if body["failureCode"] != nil || body["validatedAt"] != body["updatedAt"] {
					t.Fatal(body)
				}
			} else if body["validatedAt"] != nil {
				t.Fatal("absent timestamp not null")
			}
			if (state == media.StatusUploadedUnvalidated || state == media.StatusValidating) && body["failureCode"] != nil {
				t.Fatal("absent failure not null")
			}
		})
	}
}

func TestMediaStatusErrorsAndAuthBeforeRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, id string
		auth     bool
		err      error
		want     int
		calls    int
	}{
		{"unauthenticated", "asset", false, nil, 401, 0},
		{"invalid", "bad%20id", true, nil, 400, 0},
		{"long", strings.Repeat("a", 129), true, nil, 400, 0},
		{"absent or unowned", "asset", true, media.ErrStatusNotFound, 404, 1},
		{"dependency", "asset", true, errors.New("postgres://secret-password/private-bucket"), 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reader := statusReaderFunc(func(context.Context, string) (media.AssetStatus, error) { calls++; return media.AssetStatus{}, tc.err })
			var logs bytes.Buffer
			mux := http.NewServeMux()
			NewMediaStatusHandler(reader, slog.New(slog.NewTextHandler(&logs, nil))).Register(mux)
			r := httptest.NewRequest("GET", "/media/"+tc.id+"/status", nil)
			r.Header.Set("Upload-Token", "not-authentication")
			if tc.auth {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "owner"}))
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.want || calls != tc.calls || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
			if strings.Contains(w.Body.String()+logs.String(), "secret-password") || strings.Contains(w.Body.String()+logs.String(), "private-bucket") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestMediaStatusUnknownFailureCode(t *testing.T) {
	t.Parallel()
	code := "raw secret storage key"
	h := NewMediaStatusHandler(statusReaderFunc(func(context.Context, string) (media.AssetStatus, error) {
		return media.AssetStatus{AssetID: "asset", Status: media.StatusRejected, UpdatedAt: time.Now(), FailureCode: &code}, nil
	}), nil)
	r := httptest.NewRequest("GET", "/media/asset/status", nil)
	r.SetPathValue("assetId", "asset")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "owner"}))
	w := httptest.NewRecorder()
	h.Get(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), code) || !strings.Contains(w.Body.String(), `"failureCode":"VALIDATION_FAILED"`) {
		t.Fatal(w.Body.String())
	}
}
