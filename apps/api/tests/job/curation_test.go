package job_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

type curationStoreStub struct {
	claim               curation.Claim
	claimErr, finishErr error
	pub                 *curation.Publication
	code                string
	retry               bool
	claims, finishes    int
	finishContextError  error
}

func (s *curationStoreStub) Claim(_ context.Context, _ string, token string) (curation.Claim, error) {
	s.claims++
	c := s.claim
	c.Token = token
	return c, s.claimErr
}
func (s *curationStoreStub) Finish(ctx context.Context, _ curation.Claim, pub *curation.Publication, code string, retry bool) error {
	s.pub, s.code, s.retry = pub, code, retry
	s.finishes++
	s.finishContextError = ctx.Err()
	return s.finishErr
}
func (s *curationStoreStub) Recover(context.Context, int) (int, error) { return 0, nil }

type curatorStub struct {
	calls     int
	input     curation.Request
	err       error
	proposals []curation.Proposal
	cancel    context.CancelFunc
}

func (c *curatorStub) Curate(_ context.Context, input curation.Request) ([]curation.Proposal, error) {
	c.calls++
	c.input = input
	if c.cancel != nil {
		c.cancel()
		return nil, context.Canceled
	}
	return c.proposals, c.err
}

func curationFixture(t *testing.T, empty bool) (curation.Claim, []byte) {
	t.Helper()
	doc := transcription.Document{
		SchemaVersion: 1, AssetID: "curate-test", SourceDurationMs: 1000,
		Words: []transcription.Word{{Text: "private-transcript", StartMs: 0, EndMs: 700}, {Text: "two", StartMs: 100, EndMs: 400}},
	}
	if empty {
		doc.Words = []transcription.Word{}
	}
	data, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return curation.Claim{
		AssetID: doc.AssetID, UserID: "owner", Token: "token", Attempts: 1,
		TranscriptBucket: "private-bucket", TranscriptKey: "dev-uploads/curate-test/derived/v1/output/transcript.json",
		TranscriptSHA256: hex.EncodeToString(hash[:]), TranscriptSizeBytes: int64(len(data)), TranscriptWordCount: len(doc.Words),
		SourceDurationMs: 1000, CandidateCount: 2, MinDurationMs: 100, MaxDurationMs: 1000,
		Model: curation.Model, PromptVersion: curation.PromptVersion,
	}, data
}

func curationHandler(t *testing.T, s *curationStoreStub, d processLogDownloader, c *curatorStub, logs *bytes.Buffer) (*job.CurationHandler, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	h, err := job.NewCurationHandler(s, d, c, root, "private-bucket", slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := h.Drain(ctx); err != nil {
			t.Error(err)
		}
	})
	return h, root
}
func curationTask() *asynq.Task {
	return asynq.NewTask(curation.TaskType, []byte(`{"assetId":"curate-test"}`))
}

func TestCurationHandlerSuccessAndEmpty(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "proposals", true: "empty"}[empty], func(t *testing.T) {
			claim, data := curationFixture(t, empty)
			s := &curationStoreStub{claim: claim}
			c := &curatorStub{proposals: []curation.Proposal{{StartWordIndex: 0, EndWordIndexExclusive: 2, Title: "Title", Hook: "Hook", Reason: "Reason"}}}
			var logs bytes.Buffer
			h, root := curationHandler(t, s, processLogDownloader{data: data}, c, &logs)
			if err := h.ProcessTask(context.Background(), curationTask()); err != nil {
				t.Fatal(err)
			}
			if s.pub == nil || s.finishes != 1 || s.code != "" || s.retry {
				t.Fatal("success not published")
			}
			if empty && c.calls != 0 {
				t.Fatal("empty transcript called the LLM")
			}
			if !empty && (c.calls != 1 || c.input.TranscriptHash != "sha256:"+claim.TranscriptSHA256 ||
				c.input.Words[0].Text != "private-transcript" || c.input.Words[1].EndMs != 400 ||
				c.input.Constraints.CandidateCount != 2) {
				t.Fatal("wrong private request projection")
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatal("temporary file leaked")
			}
			for _, secret := range []string{"private-transcript", claim.TranscriptKey, claim.UserID, claim.TranscriptSHA256} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("private content in logs")
				}
			}
		})
	}
}

func TestCurationHandlerFailureOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		downloadErr, providerErr, finishErr error
		changed, invalid                    bool
		code                                string
		retry, wantErr                      bool
	}{
		{name: "download", downloadErr: errors.New("secret"), code: "CURATION_DOWNLOAD_FAILED", retry: true},
		{name: "missing", downloadErr: storage.ErrObjectNotFound, code: "CURATION_SOURCE_CHANGED"},
		{name: "checksum", changed: true, code: "CURATION_SOURCE_CHANGED"},
		{name: "auth", providerErr: curation.ErrServiceAuth, code: "CURATION_PROVIDER_AUTH"},
		{name: "rejected", providerErr: curation.ErrServiceRejected, code: "CURATION_PROVIDER_REJECTED"},
		{name: "too large", providerErr: curation.ErrInputTooLarge, code: "CURATION_INPUT_TOO_LARGE"},
		{name: "invalid service body", providerErr: curation.ErrResult, code: "CURATION_INVALID_OUTPUT"},
		{name: "timeout", providerErr: context.DeadlineExceeded, code: "CURATION_TIMEOUT", retry: true},
		{name: "unavailable", providerErr: errors.New("secret"), code: "CURATION_PROVIDER_FAILED", retry: true},
		{name: "invalid ranges", invalid: true, code: "CURATION_INVALID_OUTPUT"},
		{name: "finish failure", finishErr: errors.New("secret"), wantErr: true},
		{name: "stale", finishErr: curation.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim, data := curationFixture(t, false)
			if tc.changed {
				claim.TranscriptSHA256 = strings.Repeat("d", 64)
			}
			s := &curationStoreStub{claim: claim, finishErr: tc.finishErr}
			c := &curatorStub{err: tc.providerErr, proposals: []curation.Proposal{}}
			if tc.invalid {
				c.proposals = []curation.Proposal{{StartWordIndex: 0, EndWordIndexExclusive: 99, Title: "x", Hook: "x", Reason: "x"}}
			}
			var logs bytes.Buffer
			h, root := curationHandler(t, s, processLogDownloader{data: data, err: tc.downloadErr}, c, &logs)
			err := h.ProcessTask(context.Background(), curationTask())
			if (err != nil) != tc.wantErr || s.code != tc.code || s.retry != tc.retry {
				t.Fatalf("code=%s retry=%t err=%v", s.code, s.retry, err)
			}
			if (tc.changed || tc.downloadErr != nil) && c.calls != 0 {
				t.Fatal("unverified input sent to LLM")
			}
			if strings.Contains(logs.String(), "secret") || err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("raw dependency error leaked")
			}
			files, readErr := os.ReadDir(root)
			if readErr != nil || len(files) != 0 {
				t.Fatal("failure leaked temporary files")
			}
		})
	}
}

func TestCurationHandlerCancellationAndBadTasks(t *testing.T) {
	claim, data := curationFixture(t, false)
	s := &curationStoreStub{claim: claim}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &curatorStub{cancel: cancel}
	var logs bytes.Buffer
	h, _ := curationHandler(t, s, processLogDownloader{data: data}, c, &logs)
	if err := h.ProcessTask(ctx, curationTask()); err != nil || s.code != "CURATION_INTERRUPTED" || !s.retry || s.finishContextError != nil {
		t.Fatalf("canceled attempt did not persist with fresh context: %v", err)
	}
	previous := s.claims
	for _, payload := range []string{`null`, `{"assetId":"../bad"}`, `{"assetId":"asset","key":"secret"}`, `{"assetId":"asset"} {}`, strings.Repeat("x", 1025)} {
		if err := h.ProcessTask(context.Background(), asynq.NewTask(curation.TaskType, []byte(payload))); !errors.Is(err, asynq.SkipRetry) {
			t.Fatal("invalid task accepted")
		}
	}
	if s.claims != previous {
		t.Fatal("invalid tasks touched DB")
	}
	if err := h.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.ProcessTask(context.Background(), curationTask()); err == nil || s.claims != previous {
		t.Fatal("drained handler accepted work")
	}
}

func TestCurationDedicatedQueue(t *testing.T) {
	cfg := job.CurationWorkerConfig(nil)
	if cfg.Concurrency != 1 || len(cfg.Queues) != 1 || cfg.Queues[curation.Queue] != 1 {
		t.Fatal("wrong queue/concurrency")
	}
	for _, other := range []asynq.Config{job.ProcessWorkerConfig(nil), job.TranscriptionWorkerConfig(nil)} {
		if _, exists := other.Queues[curation.Queue]; exists {
			t.Fatal("another worker steals curation tasks")
		}
	}
}

func TestCurationHandlerDoesNotProcessUnclaimedTasks(t *testing.T) {
	for _, claimErr := range []error{nil, curation.ErrBusy, errors.New("private-db-error")} {
		store := &curationStoreStub{claimErr: claimErr}
		curator := &curatorStub{}
		var logs bytes.Buffer
		h, _ := curationHandler(t, store, processLogDownloader{err: errors.New("should not download")}, curator, &logs)
		err := h.ProcessTask(context.Background(), curationTask())
		wantErr := claimErr != nil && !errors.Is(claimErr, curation.ErrBusy)
		if (err != nil) != wantErr || curator.calls != 0 || store.finishes != 0 {
			t.Fatal("unclaimed work processed")
		}
		if strings.Contains(logs.String(), "private-db-error") || err != nil && strings.Contains(err.Error(), "private-db-error") {
			t.Fatal("private DB error exposed")
		}
	}
}

type curatorFunc func(context.Context, curation.Request) ([]curation.Proposal, error)

func (f curatorFunc) Curate(ctx context.Context, r curation.Request) ([]curation.Proposal, error) {
	return f(ctx, r)
}

func TestCurationDrainDoesNotOverlapCanceledInvocation(t *testing.T) {
	claim, data := curationFixture(t, false)
	store := &curationStoreStub{claim: claim}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	curator := curatorFunc(func(ctx context.Context, _ curation.Request) ([]curation.Proposal, error) {
		close(entered)
		<-ctx.Done()
		// Simulate a dependency which acknowledges cancellation late. The
		// serial slot must stay held until this actual invocation returns.
		<-release
		return nil, context.Canceled
	})
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h, err := job.NewCurationHandler(store, processLogDownloader{data: data}, curator, root, "private-bucket", slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- h.ProcessTask(context.Background(), curationTask()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("service call not reached")
	}
	go func() { second <- h.ProcessTask(context.Background(), curationTask()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain returned before invocation stopped: %v", err)
	}
	unblock()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err == nil {
		t.Fatal("waiting task ran during shutdown")
	}
	if err := h.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.claims != 1 || store.finishes != 1 || store.code != "CURATION_INTERRUPTED" {
		t.Fatal("shutdown duplicated claims or lost outcome")
	}
}
