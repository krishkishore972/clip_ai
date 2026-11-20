package job_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

type preparationStoreDouble struct {
	claim                      preparation.Claim
	outcome                    preparation.Outcome
	claimErr, finishErr        error
	claims, finishes, archived int
	deliveries                 []preparation.Delivery
}

func (s *preparationStoreDouble) Claim(ctx context.Context, id, token string, lease time.Duration) (preparation.Claim, error) {
	s.claims++
	if len(token) != 32 || lease <= 0 {
		panic("invalid claim")
	}
	return s.claim, s.claimErr
}
func (s *preparationStoreDouble) Finish(_ context.Context, _ preparation.Claim, out preparation.Outcome) error {
	s.finishes++
	s.outcome = out
	return s.finishErr
}
func (s *preparationStoreDouble) Recover(context.Context, int) (int, error) { return 0, nil }
func (s *preparationStoreDouble) ArchiveCandidates(context.Context, string, int) ([]preparation.Delivery, error) {
	return s.deliveries, nil
}
func (s *preparationStoreDouble) FailArchived(context.Context, preparation.Delivery) error {
	s.archived++
	return nil
}

type preparationDownloadDouble struct {
	err   error
	calls int
}

func (d *preparationDownloadDouble) Download(ctx context.Context, key, path string, size int64) error {
	d.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("missing download deadline")
	}
	if d.err != nil {
		return d.err
	}
	return os.WriteFile(path, []byte("source"), 0600)
}

type preparationUploadDouble struct {
	calls int
	err   error
}

func (u *preparationUploadDouble) UploadDerived(ctx context.Context, asset, output, kind, path string) (storage.DerivedObject, error) {
	u.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("missing upload deadline")
	}
	if u.err != nil {
		return storage.DerivedObject{}, u.err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return storage.DerivedObject{}, err
	}
	return storage.DerivedObject{Key: "dev-uploads/" + asset + "/derived/v1/" + output + "/" + kind, SizeBytes: int64(len(data)), SHA256: preparationDigest(data)}, nil
}
func preparationDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func preparationFixture(ctx context.Context, source, root string, _ time.Duration) (media.PreparedMedia, error) {
	if _, ok := ctx.Deadline(); !ok {
		return media.PreparedMedia{}, errors.New("missing encode deadline")
	}
	audio := filepath.Join(root, "audio.wav")
	if err := os.WriteFile(audio, []byte("audio"), 0600); err != nil {
		return media.PreparedMedia{}, err
	}
	return media.PreparedMedia{SourceSHA256: preparationDigest([]byte("source")), Audio: media.PreparedAudio{Path: audio, SizeBytes: 5, SHA256: preparationDigest([]byte("audio")), DurationMs: 1000, SampleRate: 16000, Channels: 1}}, nil
}
func preparationHarness(t *testing.T, s *preparationStoreDouble, d *preparationDownloadDouble, u *preparationUploadDouble, prepare func(context.Context, string, string, time.Duration) (media.PreparedMedia, error)) (*job.PreparationHandler, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "preparation")
	h, err := job.NewPreparationHandler(s, d, u, job.PreparationOptions{Bucket: "private", TempDir: root, DownloadTimeout: time.Second, EncodeTimeout: time.Second, UploadTimeout: time.Second, TaskTimeout: 10 * time.Second, Prepare: prepare}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h, root
}
func preparationClaimFixture() preparation.Claim {
	return preparation.Claim{AssetID: "asset", Bucket: "private", Key: "original", Token: strings.Repeat("a", 32), OutputID: strings.Repeat("b", 32), SizeBytes: 6, Attempts: 1, Metadata: media.Metadata{DurationMs: 1000}}
}
func preparationTask() *asynq.Task {
	return asynq.NewTask(preparation.TaskType, []byte(`{"assetId":"asset"}`))
}

func TestPreparationPublishAndAmbiguousFinish(t *testing.T) {
	for _, finishErr := range []error{nil, preparation.ErrDatabase, preparation.ErrStale} {
		t.Run(map[bool]string{true: "success", false: fmtPreparationError(finishErr)}[finishErr == nil], func(t *testing.T) {
			s := &preparationStoreDouble{claim: preparationClaimFixture(), finishErr: finishErr}
			d := &preparationDownloadDouble{}
			u := &preparationUploadDouble{}
			h, root := preparationHarness(t, s, d, u, preparationFixture)
			err := h.ProcessTask(context.Background(), preparationTask())
			if finishErr == preparation.ErrDatabase && err == nil {
				t.Fatal("ambiguous finish must report failure")
			}
			if finishErr != preparation.ErrDatabase && err != nil {
				t.Fatal(err)
			}
			if s.finishes != 1 || s.outcome.Status != "ready" || s.outcome.Artifacts == nil || u.calls != 1 {
				t.Fatalf("incomplete publication: %+v uploads %d", s.outcome, u.calls)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary files not cleaned")
			}
			// The uploader intentionally has no delete API: ambiguous Finish must retain
			// private uploaded objects because the database transaction may have committed.
		})
	}
}
func fmtPreparationError(err error) string {
	if errors.Is(err, preparation.ErrStale) {
		return "stale"
	}
	return "ambiguous"
}

func TestPreparationStrictPayload(t *testing.T) {
	cases := []struct{ kind, payload string }{{"wrong", `{"assetId":"asset"}`}, {preparation.TaskType, `{"assetId":"asset","url":"secret"}`}, {preparation.TaskType, `{"assetId":"../x"}`}, {preparation.TaskType, `{"assetId":"asset"} true`}, {preparation.TaskType, `{"assetId":"asset"}]`}, {preparation.TaskType, `null`}, {preparation.TaskType, strings.Repeat(" ", 1025)}}
	for _, tc := range cases {
		s := &preparationStoreDouble{}
		h, _ := preparationHarness(t, s, &preparationDownloadDouble{}, &preparationUploadDouble{}, preparationFixture)
		err := h.ProcessTask(context.Background(), asynq.NewTask(tc.kind, []byte(tc.payload)))
		if !errors.Is(err, asynq.SkipRetry) || s.claims != 0 {
			t.Fatalf("invalid task accepted: %q", tc.payload)
		}
	}
}

func TestPreparationRetryAndRejection(t *testing.T) {
	for _, attempt := range []int{1, 2} {
		s := &preparationStoreDouble{claim: preparationClaimFixture()}
		s.claim.Attempts = attempt
		h, _ := preparationHarness(t, s, &preparationDownloadDouble{err: errors.New("secret signed URL")}, &preparationUploadDouble{}, preparationFixture)
		err := h.ProcessTask(context.Background(), preparationTask())
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe retry error")
		}
		if errors.Is(err, asynq.SkipRetry) != (attempt == 2) {
			t.Fatal("incorrect retry budget")
		}
		expected := "pending"
		if attempt == 2 {
			expected = "failed"
		}
		if s.outcome.Status != expected {
			t.Fatalf("status %s", s.outcome.Status)
		}
	}
	s := &preparationStoreDouble{claim: preparationClaimFixture()}
	h, _ := preparationHarness(t, s, &preparationDownloadDouble{}, &preparationUploadDouble{}, func(context.Context, string, string, time.Duration) (media.PreparedMedia, error) {
		return media.PreparedMedia{}, &media.Rejection{Code: "PREPARATION_INVALID_OUTPUT"}
	})
	if err := h.ProcessTask(context.Background(), preparationTask()); !errors.Is(err, asynq.SkipRetry) || s.outcome.Status != "failed" || s.outcome.Code != "PREPARATION_INVALID_OUTPUT" {
		t.Fatalf("permanent failure: %v %+v", err, s.outcome)
	}
}

func TestPreparationTrustedBucketAndHashes(t *testing.T) {
	s := &preparationStoreDouble{claim: preparationClaimFixture()}
	s.claim.Bucket = "other"
	d := &preparationDownloadDouble{}
	u := &preparationUploadDouble{}
	h, _ := preparationHarness(t, s, d, u, preparationFixture)
	_ = h.ProcessTask(context.Background(), preparationTask())
	if d.calls != 0 || u.calls != 0 {
		t.Fatal("untrusted bucket reached storage")
	}
	s = &preparationStoreDouble{claim: preparationClaimFixture()}
	u = &preparationUploadDouble{}
	h, _ = preparationHarness(t, s, &preparationDownloadDouble{}, u, func(ctx context.Context, src, root string, budget time.Duration) (media.PreparedMedia, error) {
		out, err := preparationFixture(ctx, src, root, budget)
		out.SourceSHA256 = strings.Repeat("0", 64)
		return out, err
	})
	if err := h.ProcessTask(context.Background(), preparationTask()); !errors.Is(err, asynq.SkipRetry) || u.calls != 0 {
		t.Fatal("source mutation was published")
	}
}

type preparationInspectorDouble struct {
	info *asynq.TaskInfo
	err  error
}

func (i preparationInspectorDouble) GetTaskInfo(string, string) (*asynq.TaskInfo, error) {
	return i.info, i.err
}
func TestPreparationArchiveRequiresEvidence(t *testing.T) {
	for _, state := range []asynq.TaskState{asynq.TaskStateArchived, asynq.TaskStatePending} {
		s := &preparationStoreDouble{deliveries: []preparation.Delivery{{AssetID: "asset", EventID: "event"}}}
		h, _ := preparationHarness(t, s, &preparationDownloadDouble{}, &preparationUploadDouble{}, preparationFixture)
		h.SetArchiveInspector(preparationInspectorDouble{info: &asynq.TaskInfo{State: state}})
		if err := h.ReconcileArchived(context.Background()); err != nil {
			t.Fatal(err)
		}
		if (s.archived == 1) != (state == asynq.TaskStateArchived) {
			t.Fatal("archive evidence mismatch")
		}
	}
	s := &preparationStoreDouble{deliveries: []preparation.Delivery{{AssetID: "asset", EventID: "event"}}}
	h, _ := preparationHarness(t, s, &preparationDownloadDouble{}, &preparationUploadDouble{}, preparationFixture)
	h.SetArchiveInspector(preparationInspectorDouble{err: asynq.ErrTaskNotFound})
	if err := h.ReconcileArchived(context.Background()); err != nil || s.archived != 0 {
		t.Fatal("missing Redis task is not exhaustion evidence")
	}
}

func TestPreparationDrainCancelsAndRefusesNewWork(t *testing.T) {
	entered := make(chan struct{})
	s := &preparationStoreDouble{claim: preparationClaimFixture()}
	h, _ := preparationHarness(t, s, &preparationDownloadDouble{}, &preparationUploadDouble{}, func(ctx context.Context, _ string, _ string, _ time.Duration) (media.PreparedMedia, error) {
		close(entered)
		<-ctx.Done()
		return media.PreparedMedia{}, ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- h.ProcessTask(context.Background(), preparationTask()) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("canceled work unexpectedly succeeded")
	}
	if err := h.ProcessTask(context.Background(), preparationTask()); err == nil || s.claims != 1 {
		t.Fatal("drained handler accepted task")
	}
}

func TestPreparationCleanupOnlyExpiredOwnedAttempts(t *testing.T) {
	h, root := preparationHarness(t, &preparationStoreDouble{}, &preparationDownloadDouble{}, &preparationUploadDouble{}, preparationFixture)
	expired := filepath.Join(root, "attempt-1-dead")
	active := filepath.Join(root, "attempt-9999999999-live")
	unrelated := filepath.Join(root, "unrelated")
	for _, path := range []string{expired, active, unrelated} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	h.RemoveAttemptDirectory(outside)
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("cleanup escaped root")
	}
	if err := h.ScavengeAttempts(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired attempt retained")
	}
	for _, path := range []string{active, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("live or unrelated path removed")
		}
	}
}

func TestPreparationWorkerIsolation(t *testing.T) {
	cfg := job.PreparationWorkerConfig(nil)
	if cfg.Concurrency != 1 || len(cfg.Queues) != 1 || cfg.Queues[preparation.Queue] != 1 {
		t.Fatal("preparation queue not isolated")
	}
	if cfg.RetryDelayFunc(1, nil, nil) != time.Minute {
		t.Fatal("retry not delayed")
	}
}
