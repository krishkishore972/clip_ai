package job_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

// This intentionally uses the production encoder, not the injected encoder
// double: output-path ownership must agree across the media/job boundary.
func TestPreparationHandlerRealEncoder(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("local FFmpeg tools are required")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	source := filepath.Join(t.TempDir(), "fixture.mp4")
	command := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-map", "0:v:0", "-map", "1:a:0", "-c:v", "libx264", "-threads", "1",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", source)
	if err := command.Run(); err != nil {
		t.Fatalf("local media fixture generation failed: %v", err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := media.ProbeFile(ctx, source, 10*time.Second)
	if err != nil {
		t.Fatal("fixture validation failed")
	}
	claim := preparationClaimFixture()
	claim.SizeBytes = int64(len(original))
	claim.Metadata = metadata
	store := &preparationStoreDouble{claim: claim}
	uploader := &realPreparationUploadRecorder{objects: make(map[string]storage.DerivedObject)}
	root := filepath.Join(t.TempDir(), "preparation")
	handler, err := job.NewPreparationHandler(store, realPreparationDownloader{source: source}, uploader, job.PreparationOptions{
		Bucket: claim.Bucket, TempDir: root,
		DownloadTimeout: 5 * time.Second, EncodeTimeout: time.Minute,
		UploadTimeout: 5 * time.Second, TaskTimeout: 90 * time.Second,
		// Prepare is deliberately nil to exercise production media.PrepareFile.
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.ProcessTask(ctx, preparationTask()); err != nil {
		t.Fatalf("real preparation handler failed: %v", err)
	}
	if store.finishes != 1 || store.outcome.Status != "ready" || store.outcome.Artifacts == nil {
		t.Fatalf("not atomically ready: %+v", store.outcome)
	}
	artifacts := store.outcome.Artifacts
	audio, audioOK := uploader.objects["audio"]
	if !audioOK || len(uploader.objects) != 1 {
		t.Fatal("verified audio output was not uploaded")
	}
	if artifacts.SourceSHA256 != preparationDigest(original) ||
		artifacts.AudioSHA256 != audio.SHA256 ||
		artifacts.AudioSizeBytes != audio.SizeBytes ||
		artifacts.AudioKey != audio.Key {
		t.Fatal("published artifacts differ from verified source/upload bytes")
	}
	after, err := os.ReadFile(source)
	if err != nil || preparationDigest(after) != preparationDigest(original) {
		t.Fatal("source fixture was modified")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("real encoder attempt was not cleaned")
	}
}

type realPreparationDownloader struct{ source string }

func (d realPreparationDownloader) Download(ctx context.Context, key, path string, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key != "original" {
		return errors.New("unexpected source key")
	}
	data, err := os.ReadFile(d.source)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("unexpected source size")
	}
	return os.WriteFile(path, data, 0600)
}

type realPreparationUploadRecorder struct {
	objects map[string]storage.DerivedObject
}

func (u *realPreparationUploadRecorder) UploadDerived(ctx context.Context, asset, output, kind, path string) (storage.DerivedObject, error) {
	if err := ctx.Err(); err != nil {
		return storage.DerivedObject{}, err
	}
	if kind != "audio" {
		return storage.DerivedObject{}, errors.New("unexpected derived kind")
	}
	extension := ".wav"
	data, err := os.ReadFile(path)
	if err != nil {
		return storage.DerivedObject{}, err
	}
	object := storage.DerivedObject{Key: "dev-uploads/" + asset + "/derived/v1/" + output + "/" + kind + extension, SizeBytes: int64(len(data)), SHA256: preparationDigest(data)}
	u.objects[kind] = object
	return object, nil
}
