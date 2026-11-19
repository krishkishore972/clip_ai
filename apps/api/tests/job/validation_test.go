package job_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

type validationStoreStub struct {
	claim   func(context.Context, string, string, time.Duration) (job.ValidationAsset, error)
	finish  func(context.Context, job.ValidationAsset, job.ValidationOutcome) error
	recover func(context.Context, int) (int, error)
}

func (s validationStoreStub) Claim(ctx context.Context, id, token string, lease time.Duration) (job.ValidationAsset, error) {
	return s.claim(ctx, id, token, lease)
}
func (s validationStoreStub) Finish(ctx context.Context, a job.ValidationAsset, o job.ValidationOutcome) error {
	return s.finish(ctx, a, o)
}
func (s validationStoreStub) Recover(ctx context.Context, n int) (int, error) {
	if s.recover == nil {
		return 0, nil
	}
	return s.recover(ctx, n)
}

type downloadStub func(context.Context, string, string, int64) error

func (f downloadStub) Download(ctx context.Context, key, path string, size int64) error {
	return f(ctx, key, path, size)
}

func fixtureMetadata() media.Metadata {
	return media.Metadata{
		Container: "mp4", SizeBytes: 4, DurationMs: 1000,
		Video: []media.VideoStream{{Index: 0, Codec: "h264", Width: 64, Height: 48, FPS: media.Rational{Num: 24, Den: 1}, RotationDegrees: 90}},
		Audio: []media.AudioStream{{Index: 1, Codec: "aac", SampleRate: 48000, Channels: 2}},
	}
}

// handlerFixture owns configuration without exposing
// production handler fields. Configure immutable options before construction.
type handlerFixture struct {
	*job.ValidationHandler
	options job.ValidationOptions
}

func testHandler(t *testing.T, store job.ValidationStore, download job.VideoDownloader, configure ...func(*job.ValidationOptions)) *handlerFixture {
	t.Helper()
	fixture := &handlerFixture{}
	options := job.ValidationOptions{
		Bucket: "trusted-bucket", TempDir: filepath.Join(t.TempDir(), "worker"), DownloadTimeout: time.Second,
		ProbeTimeout: time.Second, TaskTimeout: 3 * time.Second,
	}
	for _, apply := range configure {
		apply(&options)
	}
	h, err := job.NewValidationHandler(store, download, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	options.TempDir, err = filepath.EvalSymlinks(options.TempDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ValidationHandler, fixture.options = h, options
	return fixture
}

func TestValidationPayload(t *testing.T) {
	var claimed string
	h := testHandler(t, validationStoreStub{claim: func(_ context.Context, id, _ string, _ time.Duration) (job.ValidationAsset, error) {
		claimed = id
		return job.ValidationAsset{}, nil
	}}, downloadStub(func(context.Context, string, string, int64) error { t.Fatal("unexpected download"); return nil }))
	for _, payload := range []string{"", "null", `{}`, `{"assetId":"../secret"}`, `{"assetId":"a","url":"https://untrusted"}`, `{"assetId":"a"} {}`, `{"assetId":123}`, strings.Repeat("x", 1025)} {
		if err := h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(payload))); !errors.Is(err, asynq.SkipRetry) {
			t.Fatalf("accepted malformed payload %q", payload)
		}
	}
	if err := h.ProcessTask(context.Background(), nil); !errors.Is(err, asynq.SkipRetry) {
		t.Fatal("nil task accepted")
	}
	if err := h.ProcessTask(context.Background(), asynq.NewTask("unknown", []byte(`{"assetId":"a"}`))); !errors.Is(err, asynq.SkipRetry) {
		t.Fatal("wrong task type accepted")
	}
	if err := h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset-123"}`))); err != nil || claimed != "asset-123" {
		t.Fatal("valid payload rejected")
	}
	for _, id := range []string{"", "../secret", strings.Repeat("x", 129)} {
		if job.ValidValidationAssetID(id) {
			t.Fatalf("unsafe ID accepted: %q", id)
		}
	}
	if !job.ValidValidationAssetID("asset-123") {
		t.Fatal("valid ID rejected")
	}
}

func TestValidationOutcomesAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, status, code string
		attempts           int
		downloadErr        error
		probeErr           error
		bucket             string
		size               int64
		finishErr          error
		wantErr, skip      bool
	}{
		{name: "ready", status: "ready"},
		{name: "permanent rejection", status: "rejected", code: "AUDIO_REQUIRED", probeErr: &media.Rejection{Code: "AUDIO_REQUIRED"}},
		{name: "wrong size", status: "rejected", code: "SIZE_MISMATCH", downloadErr: storage.ErrDownloadSizeMismatch},
		{name: "download retries", status: "uploaded_unvalidated", code: "DOWNLOAD_FAILED", downloadErr: errors.New("private-provider-secret"), wantErr: true},
		{name: "download timeout", status: "uploaded_unvalidated", code: "DOWNLOAD_TIMEOUT", downloadErr: storage.ErrDownloadTimeout, wantErr: true},
		{name: "missing object retries", status: "uploaded_unvalidated", code: "SOURCE_NOT_FOUND", downloadErr: storage.ErrObjectNotFound, wantErr: true},
		{name: "probe timeout retries", status: "uploaded_unvalidated", code: "PROBE_TIMEOUT", probeErr: media.ErrProbeTimeout, wantErr: true},
		{name: "probe unavailable retries", status: "uploaded_unvalidated", code: "PROBE_UNAVAILABLE", probeErr: media.ErrProbeUnavailable, wantErr: true},
		{name: "fourth failure stops", status: "validation_failed", code: "DOWNLOAD_FAILED", attempts: 4, downloadErr: storage.ErrDownloadUnavailable, wantErr: true, skip: true},
		{name: "foreign bucket", status: "uploaded_unvalidated", code: "STORAGE_CONFIGURATION", bucket: "other-bucket", wantErr: true},
		{name: "oversize", status: "rejected", code: "FILE_TOO_LARGE", size: media.MaxFileBytes + 1},
		{name: "stale result ignored", status: "ready", finishErr: job.ErrValidationStale},
		{name: "DB result failure", status: "ready", finishErr: errors.New("postgres://secret"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := tc.attempts
			if attempts == 0 {
				attempts = 1
			}
			bucket := tc.bucket
			if bucket == "" {
				bucket = "trusted-bucket"
			}
			size := tc.size
			if size == 0 {
				size = 4
			}
			finished := false
			var path string
			store := validationStoreStub{
				claim: func(ctx context.Context, id, token string, lease time.Duration) (job.ValidationAsset, error) {
					if id != "asset" || len(token) != 32 || lease <= 3*time.Second {
						t.Error("claim identity/lease invalid")
					}
					return job.ValidationAsset{ID: id, Token: token, Bucket: bucket, Key: "trusted-key", SizeBytes: size, Attempts: attempts}, nil
				},
				finish: func(ctx context.Context, a job.ValidationAsset, o job.ValidationOutcome) error {
					finished = true
					if ctx.Err() != nil || o.Status != tc.status || o.Code != tc.code {
						t.Errorf("outcome=%+v context=%v", o, ctx.Err())
					}
					if o.Status == "ready" && (o.Metadata == nil || o.Metadata.Video[0].RotationDegrees != 90) {
						t.Error("lost validated metadata")
					}
					return tc.finishErr
				},
			}
			h := testHandler(t, store, downloadStub(func(ctx context.Context, key, destination string, expected int64) error {
				path = destination
				if key != "trusted-key" || expected != 4 {
					t.Error("untrusted download parameters")
				}
				if err := os.WriteFile(destination, []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
				return tc.downloadErr
			}), func(options *job.ValidationOptions) {
				options.Probe = func(ctx context.Context, p string, timeout time.Duration) (media.Metadata, error) {
					if p != path || timeout != time.Second {
						t.Error("unexpected probe arguments")
					}
					return fixtureMetadata(), tc.probeErr
				}
			})
			err := h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset"}`)))
			if !finished || (err != nil) != tc.wantErr || errors.Is(err, asynq.SkipRetry) != tc.skip {
				t.Fatalf("finished=%v err=%v", finished, err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "postgres://")) {
				t.Fatal("raw error leaked")
			}
			entries, err := os.ReadDir(h.options.TempDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary data not cleaned up")
			}
		})
	}
}

func TestValidationSkipsWithoutDownloading(t *testing.T) {
	for _, claimErr := range []error{nil, job.ErrValidationBusy, errors.New("private-db-secret")} {
		store := validationStoreStub{claim: func(context.Context, string, string, time.Duration) (job.ValidationAsset, error) {
			return job.ValidationAsset{}, claimErr
		}}
		h := testHandler(t, store, downloadStub(func(context.Context, string, string, int64) error {
			t.Fatal("unexpected download")
			return nil
		}))
		err := h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset"}`)))
		if (err != nil) != (claimErr != nil && !errors.Is(claimErr, job.ErrValidationBusy)) {
			t.Fatalf("claim=%v handler=%v", claimErr, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Fatal("claim error leaked")
		}
	}
}

func TestValidationCancellationBookkeeping(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := false
	store := validationStoreStub{
		claim: func(_ context.Context, id, token string, _ time.Duration) (job.ValidationAsset, error) {
			return job.ValidationAsset{ID: id, Token: token, Bucket: "trusted-bucket", Key: "key", SizeBytes: 4, Attempts: 1}, nil
		},
		finish: func(ctx context.Context, _ job.ValidationAsset, o job.ValidationOutcome) error {
			finished = true
			if ctx.Err() != nil || o.Code != "VALIDATION_INTERRUPTED" || o.Status != "uploaded_unvalidated" {
				t.Errorf("canceled bookkeeping: %v %+v", ctx.Err(), o)
			}
			return nil
		},
	}
	h := testHandler(t, store, downloadStub(func(ctx context.Context, _, path string, _ int64) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}))
	if err := h.ProcessTask(parent, asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset"}`))); err == nil || !finished {
		t.Fatal("cancellation did not record retryable outcome")
	}
}

func TestValidationTemporaryScavenging(t *testing.T) {
	h := testHandler(t, validationStoreStub{}, downloadStub(func(context.Context, string, string, int64) error { return nil }))
	old := filepath.Join(h.options.TempDir, "attempt-1-old")
	future := filepath.Join(h.options.TempDir, "attempt-9999999999-future")
	unrelated := filepath.Join(h.options.TempDir, "leave-me")
	for _, path := range []string{old, future, unrelated} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.options.TempDir, "attempt-1-link")); err != nil {
		t.Fatal(err)
	}
	if err := h.ScavengeAttempts(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired attempt remains")
	}
	for _, path := range []string{future, unrelated, outside} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("cleanup touched unrelated/active directory")
		}
	}
	h.RemoveAttemptDirectory(outside)
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("cleanup escaped private root")
	}
}

func TestValidationDrainJoinsCanceledHandler(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	store := validationStoreStub{
		claim: func(_ context.Context, id, token string, _ time.Duration) (job.ValidationAsset, error) {
			return job.ValidationAsset{ID: id, Token: token, Bucket: "trusted-bucket", Key: "key", SizeBytes: 4, Attempts: 1}, nil
		},
		finish: func(context.Context, job.ValidationAsset, job.ValidationOutcome) error { return nil },
	}
	h := testHandler(t, store, downloadStub(func(ctx context.Context, _, _ string, _ int64) error {
		close(started)
		<-release
		return errors.New("download interrupted")
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset"}`)))
	}()
	<-started
	short, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Drain(short); !errors.Is(err, context.Canceled) {
		t.Fatal("drain did not wait for active handler")
	}
	if err := h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"other"}`))); err == nil {
		t.Fatal("draining worker accepted another invocation")
	}
	close(release)
	<-done
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := h.Drain(ctx); err != nil {
		t.Fatal("drain failed after handler completed")
	}
}

func TestValidationDownloadDeadline(t *testing.T) {
	finished := false
	store := validationStoreStub{
		claim: func(_ context.Context, id, token string, _ time.Duration) (job.ValidationAsset, error) {
			return job.ValidationAsset{ID: id, Token: token, Bucket: "trusted-bucket", Key: "key", SizeBytes: 4, Attempts: 1}, nil
		},
		finish: func(ctx context.Context, _ job.ValidationAsset, outcome job.ValidationOutcome) error {
			finished = true
			if ctx.Err() != nil || outcome.Code != "DOWNLOAD_TIMEOUT" || outcome.Status != "uploaded_unvalidated" {
				t.Errorf("unexpected timeout outcome %+v %v", outcome, ctx.Err())
			}
			return nil
		},
	}
	h := testHandler(t, store, downloadStub(func(ctx context.Context, _, _ string, _ int64) error {
		<-ctx.Done()
		return ctx.Err()
	}), func(options *job.ValidationOptions) { options.DownloadTimeout = 10 * time.Millisecond })
	start := time.Now()
	if err := h.ProcessTask(context.Background(), asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset"}`))); err == nil || !finished || time.Since(start) > time.Second {
		t.Fatal("download deadline not enforced")
	}
}

func TestValidationTemporaryRootGuards(t *testing.T) {
	newWithRoot := func(root string) (*job.ValidationHandler, error) {
		return job.NewValidationHandler(validationStoreStub{}, downloadStub(func(context.Context, string, string, int64) error { return nil }), job.ValidationOptions{
			Bucket: "trusted-bucket", TempDir: root, DownloadTimeout: time.Second, ProbeTimeout: time.Second, TaskTimeout: 3 * time.Second,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	for _, root := range []string{"", "relative", "/"} {
		if _, err := newWithRoot(root); err == nil {
			t.Fatal("unsafe temporary root accepted")
		}
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := newWithRoot(root); err == nil {
		t.Fatal("shared directory accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := newWithRoot(link); err == nil {
		t.Fatal("symlink directory accepted")
	}
}
