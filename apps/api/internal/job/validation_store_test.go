package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/media"
)

func TestValidationStoreDefensiveArguments(t *testing.T) {
	store := SQLValidationStore{}
	ctx := context.Background()
	for _, tc := range []struct {
		id, token string
		lease     time.Duration
	}{
		{"", "token", time.Minute},
		{"asset", " ", time.Minute},
		{"asset", "token", 0},
	} {
		if _, err := store.Claim(ctx, tc.id, tc.token, tc.lease); !errors.Is(err, errValidationArguments) {
			t.Fatalf("invalid claim: %v", err)
		}
	}
	asset := ValidationAsset{ID: "asset", Token: "token"}
	for _, outcome := range []ValidationOutcome{
		{Status: "ready"},
		{Status: "validating", Code: "FAILED"},
		{Status: "rejected", Code: "raw provider error"},
		{Status: "validation_failed"},
		{Status: "uploaded_unvalidated", Code: "FAILED", Metadata: &media.Metadata{}},
		{Status: "ready", Metadata: &media.Metadata{DurationMs: 1}},
	} {
		if err := store.Finish(ctx, asset, outcome); !errors.Is(err, errValidationArguments) {
			t.Fatalf("invalid outcome %+v: %v", outcome, err)
		}
	}
	if _, err := store.Recover(ctx, 0); !errors.Is(err, errValidationArguments) {
		t.Fatalf("invalid recovery: %v", err)
	}
	if _, err := store.Claim(ctx, "asset", "token", time.Minute); !errors.Is(err, ErrValidationDatabase) {
		t.Fatalf("nil DB claim: %v", err)
	}
	if err := store.Finish(ctx, asset, ValidationOutcome{Status: "rejected", Code: "INVALID_MEDIA"}); !errors.Is(err, ErrValidationDatabase) {
		t.Fatalf("nil DB finish: %v", err)
	}
	if _, err := store.Recover(ctx, 1); !errors.Is(err, ErrValidationDatabase) {
		t.Fatalf("nil DB recovery: %v", err)
	}
}

func TestValidationDatabaseErrorsAreSanitized(t *testing.T) {
	ctx := context.Background()
	if err := validationDBError(ctx, ctx); !errors.Is(err, ErrValidationDatabase) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := validationDBError(cancelled, ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
