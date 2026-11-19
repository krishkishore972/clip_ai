package media

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
)

func TestStatusReaderPreconditions(t *testing.T) {
	t.Parallel()
	reader := SQLStatusReader{}
	owned := auth.WithPrincipal(context.Background(), auth.Principal{UserID: "owner"})
	for _, tc := range []struct {
		name string
		ctx  context.Context
		id   string
		want error
	}{
		{"unauthenticated before dependency", context.Background(), "asset", ErrStatusUnauthenticated},
		{"empty", owned, "", ErrStatusInvalid},
		{"slash", owned, "a/b", ErrStatusInvalid},
		{"too long", owned, strings.Repeat("a", 129), ErrStatusInvalid},
		{"missing dependency", owned, "a-Z_0.:1", ErrStatusUnavailable},
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
		in := AssetStatus{AssetID: "asset", Status: StatusValidationFailed, Attempts: 4, UpdatedAt: now, FailureCode: &code}
		out, err := PublicAssetStatus(in)
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
		in.Status = StatusReady
		in.ValidatedAt = &now
		if out, err := PublicAssetStatus(in); err != nil || out.FailureCode != nil {
			t.Fatal("ready retained failure code")
		}
	}
	for _, in := range []AssetStatus{
		{AssetID: "asset", Status: "private status", UpdatedAt: now},
		{AssetID: "asset", Status: StatusValidating, Attempts: 5, UpdatedAt: now},
		{AssetID: "asset", Status: StatusValidating, Attempts: -1, UpdatedAt: now},
	} {
		if _, err := PublicAssetStatus(in); !errors.Is(err, ErrStatusUnavailable) {
			t.Fatal("corrupt projection exposed")
		}
	}
}
