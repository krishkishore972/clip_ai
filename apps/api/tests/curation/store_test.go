package curation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/curation"
)

func TestStoreRequiresDatabase(t *testing.T) {
	store := curation.SQLStore{}
	if _, err := store.Claim(context.Background(), "asset", "token"); !errors.Is(err, curation.ErrConfiguration) {
		t.Fatal("nil database accepted")
	}
	if err := store.Finish(context.Background(), curation.Claim{}, nil, "", false); !errors.Is(err, curation.ErrConfiguration) {
		t.Fatal("nil database/claim accepted")
	}
	if _, err := store.Recover(context.Background(), 1); !errors.Is(err, curation.ErrConfiguration) {
		t.Fatal("nil recovery database accepted")
	}
}
