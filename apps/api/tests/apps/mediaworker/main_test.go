package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/app/mediaworker"
)

func TestMediaWorkerDisabled(t *testing.T) {
	t.Setenv("MEDIA_PREPARATION_WORKER_ENABLED", "false")
	if err := mediaworker.Run(); err != nil {
		t.Fatal(err)
	}
}
func TestMediaWorkerSchemaFailsClosed(t *testing.T) {
	if err := mediaworker.CheckSchema(context.Background(), nil); err == nil {
		t.Fatal("nil schema accepted")
	}
}
func TestMediaWorkerTempRootsSeparated(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := mediaworker.CheckTemporaryCapacity(root, alias); err == nil {
		t.Fatal("aliased validation root accepted")
	}
	if err := mediaworker.CheckTemporaryCapacity(filepath.Join(root, "missing"), ""); err == nil {
		t.Fatal("unavailable root accepted")
	}
}
