package migrations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/handler"
	"github.com/krishkishore972/clip_ai/internal/media"
)

// Called only by the existing opt-in disposable-schema suite, after migration 5.
func testMediaStatus(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	var owner string
	if err := pool.QueryRowContext(ctx, "SELECT user_id FROM media_assets WHERE id = 'owned'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	owned := auth.WithPrincipal(ctx, auth.Principal{UserID: owner})
	reader := media.SQLStatusReader{DB: pool}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	handler.NewMediaStatusHandler(reader, logger).Register(mux)
	request := func(ctx context.Context, id string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/media/"+id+"/status", nil).WithContext(ctx))
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return w
	}
	for i, state := range []string{media.StatusUploadedUnvalidated, media.StatusValidating, media.StatusReady, media.StatusRejected, media.StatusValidationFailed} {
		id := "status-" + state
		const insert = `
			INSERT INTO media_assets (
				id, user_id, storage_bucket, storage_key, original_filename,
				declared_content_type, size_bytes, status, validation_attempts,
				failure_code, updated_at, validated_at, duration_ms,
				width, height, fps_num, fps_den, video_codec, probed_metadata
			)
			VALUES (
				$1, $2, 'private-status-bucket', $1, 'secret-original.mp4',
				'video/mp4', 1024, $3, $4,
				'raw private failure text', '2026-11-19T12:30:00+01:00',
				CASE WHEN $3 = 'ready' THEN '2026-11-19T12:30:00+01:00'::timestamptz END,
				1000, 1920, 1080, 30, 1, 'h264', '{"private":"probe metadata"}'::jsonb
			)
		`
		if _, err := pool.ExecContext(ctx, insert, id, owner, state, i); err != nil {
			t.Fatal(err)
		}
		w := request(owned, id)
		if w.Code != 200 {
			t.Fatalf("state %s: %d %s", state, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 6 || body["assetId"] != id || body["status"] != state || body["attempts"] != float64(i) || body["updatedAt"] != "2026-11-19T11:30:00Z" {
			t.Fatal(body)
		}
		if state == media.StatusReady {
			if body["failureCode"] != nil || body["validatedAt"] != body["updatedAt"] {
				t.Fatal(body)
			}
		} else if body["failureCode"] != "VALIDATION_FAILED" || body["validatedAt"] != nil {
			t.Fatal(body)
		}
		for _, private := range []string{"private-status-bucket", "secret-original", "probe metadata", "raw private failure"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private data exposed")
			}
		}
	}
	var notFoundBody string
	for _, tc := range []struct {
		ctx context.Context
		id  string
	}{
		{owned, "status-absent"}, {owned, "legacy"},
		{auth.WithPrincipal(ctx, auth.Principal{UserID: "other-user"}), "status-ready"},
	} {
		w := request(tc.ctx, tc.id)
		if w.Code != 404 {
			t.Fatalf("ownership lookup: %d %s", w.Code, w.Body.String())
		}
		if notFoundBody == "" {
			notFoundBody = w.Body.String()
		} else if w.Body.String() != notFoundBody {
			t.Fatal("absence/ownership errors distinguishable")
		}
	}
	if w := request(ctx, "status-ready"); w.Code != 401 {
		t.Fatal("unauthenticated status accepted")
	}
	if _, err := reader.GetStatus(owned, "status-absent"); !errors.Is(err, media.ErrStatusNotFound) {
		t.Fatal(err)
	}
	// Closed dependencies must return a classification, never a driver message.
	closed, err := sql.Open("pgx", "postgres://unused.invalid/test")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (media.SQLStatusReader{DB: closed}).GetStatus(owned, "status-ready"); !errors.Is(err, media.ErrStatusUnavailable) || err.Error() != media.ErrStatusUnavailable.Error() {
		t.Fatal("driver error exposed")
	}
	// Existing worker and optional queue-pipeline outcomes are read through HTTP,
	// not reproduced by a second status-only implementation.
	rows, err := pool.QueryContext(ctx, `
		SELECT id, user_id, status
		FROM media_assets
		WHERE id NOT LIKE 'status-%'
			AND user_id IS NOT NULL
			AND status IN ('ready', 'rejected', 'validation_failed')
	`)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct{ id, user, state string }
	var outcomes []outcome
	for rows.Next() {
		var out outcome
		if err := rows.Scan(&out.id, &out.user, &out.state); err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, out)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, out := range outcomes {
		w := request(auth.WithPrincipal(ctx, auth.Principal{UserID: out.user}), out.id)
		var body media.AssetStatus
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Status != out.state {
			t.Fatalf("worker outcome %s: %d %s", out.id, w.Code, w.Body.String())
		}
	}
}
