package media_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/media"
)

func TestStatusReaderPreconditions(t *testing.T) {
	t.Parallel()
	reader := media.SQLStatusReader{}
	owned := auth.WithPrincipal(context.Background(), auth.Principal{UserID: "owner"})
	for _, tc := range []struct {
		name string
		ctx  context.Context
		id   string
		want error
	}{
		{"unauthenticated before dependency", context.Background(), "asset", media.ErrStatusUnauthenticated},
		{"empty", owned, "", media.ErrStatusInvalid},
		{"slash", owned, "a/b", media.ErrStatusInvalid},
		{"too long", owned, strings.Repeat("a", 129), media.ErrStatusInvalid},
		{"missing dependency", owned, "a-Z_0.:1", media.ErrStatusUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reader.GetStatus(tc.ctx, tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestPublicStatusSanitization(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, code := range []string{"PROBE_TIMEOUT", "DURATION_LIMIT", "VALIDATION_ATTEMPTS_EXHAUSTED", "raw private object/secret"} {
		in := media.AssetStatus{AssetID: "asset", Status: media.StatusValidationFailed, Attempts: 4, UpdatedAt: now, FailureCode: &code}
		out, err := media.PublicAssetStatus(in)
		if err != nil || out.FailureCode == nil {
			t.Fatal("valid projection rejected")
		}
		want := code
		if strings.Contains(code, "private") {
			want = "VALIDATION_FAILED"
		}
		if *out.FailureCode != want {
			t.Fatal("failure code was not sanitized")
		}
		in.Status = media.StatusReady
		in.ValidatedAt = &now
		if out, err := media.PublicAssetStatus(in); err != nil || out.FailureCode != nil {
			t.Fatal("ready retained failure code")
		}
	}
	for _, in := range []media.AssetStatus{
		{AssetID: "asset", Status: "private status", UpdatedAt: now},
		{AssetID: "asset", Status: media.StatusValidating, Attempts: 5, UpdatedAt: now},
		{AssetID: "asset", Status: media.StatusValidating, Attempts: -1, UpdatedAt: now},
	} {
		if _, err := media.PublicAssetStatus(in); !errors.Is(err, media.ErrStatusUnavailable) {
			t.Fatal("corrupt projection exposed")
		}
	}
}
