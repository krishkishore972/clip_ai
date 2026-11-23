package job_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
)

type transcriptStoreStub struct {
	claim               transcription.Claim
	claimErr, finishErr error
	pub                 *transcription.Publication
	code                string
	retry               bool
	finishes            int
	finishContextError  error
}

func (s *transcriptStoreStub) Claim(context.Context, string, string, time.Duration) (transcription.Claim, error) {
	return s.claim, s.claimErr
}
func (s *transcriptStoreStub) Finish(ctx context.Context, _ transcription.Claim, pub *transcription.Publication, code string, retry bool) error {
	s.pub, s.code, s.retry = pub, code, retry
	s.finishContextError = ctx.Err()
	s.finishes++
	return s.finishErr
}
func (s *transcriptStoreStub) Recover(context.Context, int) (int, error) { return 0, nil }

type transcriptProviderStub struct {
	result   *response.PreRecordedResponse
	err      error
	calls    int
	language string
	cancel   context.CancelFunc
}

func (p *transcriptProviderStub) TranscribeFileWithLanguage(_ context.Context, path, language string) (*response.PreRecordedResponse, error) {
	p.calls++
	p.language = language
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	if p.cancel != nil {
		p.cancel()
		return nil, context.Canceled
	}
	return p.result, p.err
}

type transcriptUploaderStub struct {
	data []byte
	err  error
}

func (u *transcriptUploaderStub) UploadDerived(_ context.Context, asset, output, kind, path string) (storage.DerivedObject, error) {
	if u.err != nil {
		return storage.DerivedObject{}, u.err
	}
	if kind != "transcript" {
		return storage.DerivedObject{}, errors.New("wrong derived kind")
	}
	var err error
	u.data, err = os.ReadFile(path)
	if err != nil {
		return storage.DerivedObject{}, err
	}
	hash := sha256.Sum256(u.data)
	return storage.DerivedObject{
		Key: transcription.TranscriptKey(asset, output), SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(u.data)),
	}, nil
}

func transcriptFixture(t *testing.T) (transcription.Claim, []byte, *response.PreRecordedResponse) {
	t.Helper()
	data := bytes.Repeat([]byte{1}, 32044)
	hash := sha256.Sum256(data)
	claim := transcription.Claim{
		AssetID: "asset-test", Bucket: "private-bucket", AudioKey: "dev-uploads/asset-test/derived/v1/audio-output/audio.wav",
		AudioSHA256: hex.EncodeToString(hash[:]), AudioSizeBytes: int64(len(data)),
		AudioStartMs: -7, SourceDurationMs: 1000, Language: "multi", Token: "token", OutputID: "output", Attempts: 1,
	}
	result := &response.PreRecordedResponse{
		Metadata: &response.Metadata{Duration: 1},
		Results: &response.Result{Channels: []response.Channel{{
			Alternatives: []response.Alternative{{
				Transcript: "Private text.", Words: []response.Word{{
					Word: "private", Start: .1, End: .5, Confidence: .9,
				}},
			}},
		}}},
	}
	return claim, data, result
}

func transcriptHandler(t *testing.T, store *transcriptStoreStub, downloader processLogDownloader, uploader *transcriptUploaderStub, provider *transcriptProviderStub, logs *bytes.Buffer) (*job.TranscriptionHandler, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	handler, err := job.NewTranscriptionHandler(store, downloader, uploader, provider, root, "private-bucket", slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler, root
}

func TestTranscriptionWorkerSuccess(t *testing.T) {
	claim, data, result := transcriptFixture(t)
	store := &transcriptStoreStub{claim: claim}
	uploader := &transcriptUploaderStub{}
	provider := &transcriptProviderStub{result: result}
	var logs bytes.Buffer
	handler, root := transcriptHandler(t, store, processLogDownloader{data: data}, uploader, provider, &logs)
	task := asynq.NewTask(transcription.TaskType, []byte(`{"assetId":"asset-test"}`))
	if err := handler.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if store.pub == nil || store.pub.WordCount != 1 || store.code != "" || store.finishes != 1 ||
		provider.calls != 1 || provider.language != "multi" {
		t.Fatal("transcript was not published once")
	}
	var doc transcription.Document
	if err := json.Unmarshal(uploader.data, &doc); err != nil || doc.Words[0].StartMs != 93 {
		t.Fatal("uploaded file is not source-aligned transcript JSON")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("successful attempt leaked temporary files")
	}
	for _, private := range []string{"Private text.", claim.AudioKey, claim.Token, claim.OutputID} {
		if strings.Contains(logs.String(), private) {
			t.Fatalf("private data in log: %q", private)
		}
	}
}

func TestTranscriptionWorkerFailureOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		downloadErr, providerErr, uploadErr, finishErr error
		changed, invalid                               bool
		code                                           string
		retry, wantErr                                 bool
	}{
		{name: "download", downloadErr: errors.New("secret"), code: "TRANSCRIPTION_DOWNLOAD_FAILED", retry: true},
		{name: "checksum", changed: true, code: "TRANSCRIPTION_SOURCE_CHANGED"},
		{name: "authentication", providerErr: transcription.ErrProviderAuth, code: "TRANSCRIPTION_PROVIDER_AUTH"},
		{name: "rejected", providerErr: transcription.ErrProviderRejected, code: "TRANSCRIPTION_PROVIDER_REJECTED"},
		{name: "timeout", providerErr: context.DeadlineExceeded, code: "TRANSCRIPTION_TIMEOUT", retry: true},
		{name: "provider unavailable", providerErr: errors.New("secret"), code: "TRANSCRIPTION_PROVIDER_FAILED", retry: true},
		{name: "invalid output", invalid: true, code: "TRANSCRIPTION_INVALID_OUTPUT"},
		{name: "upload", uploadErr: errors.New("secret"), code: "TRANSCRIPTION_UPLOAD_FAILED", retry: true},
		{name: "persistence", finishErr: errors.New("secret"), wantErr: true},
		{name: "stale", finishErr: transcription.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim, data, result := transcriptFixture(t)
			if tc.changed {
				claim.AudioSHA256 = strings.Repeat("f", 64)
			}
			if tc.invalid {
				result.Results.Channels[0].Alternatives[0].Words[0].Confidence = 2
			}
			store := &transcriptStoreStub{claim: claim, finishErr: tc.finishErr}
			provider := &transcriptProviderStub{result: result, err: tc.providerErr}
			var logs bytes.Buffer
			handler, root := transcriptHandler(t, store, processLogDownloader{data: data, err: tc.downloadErr}, &transcriptUploaderStub{err: tc.uploadErr}, provider, &logs)
			err := handler.ProcessTask(context.Background(), asynq.NewTask(transcription.TaskType, []byte(`{"assetId":"asset-test"}`)))
			if (err != nil) != tc.wantErr || store.code != tc.code || store.retry != tc.retry {
				t.Fatalf("outcome code=%s retry=%t err=%v", store.code, store.retry, err)
			}
			if tc.code != "" && store.pub != nil {
				t.Fatal("failed work published a ready result")
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatal("failed attempt leaked temporary files")
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("dependency error leaked")
			}
			if (tc.changed || tc.downloadErr != nil) && provider.calls != 0 {
				t.Fatal("unverified audio sent to provider")
			}
		})
	}
}

func TestTranscriptionWorkerRejectsInvalidTasks(t *testing.T) {
	store := &transcriptStoreStub{}
	var logs bytes.Buffer
	handler, _ := transcriptHandler(t, store, processLogDownloader{}, &transcriptUploaderStub{}, &transcriptProviderStub{}, &logs)
	for _, payload := range []string{`null`, `{"assetId":"../bad"}`, `{"assetId":"asset","key":"secret"}`, `{"assetId":"asset"} {}`} {
		err := handler.ProcessTask(context.Background(), asynq.NewTask(transcription.TaskType, []byte(payload)))
		if !errors.Is(err, asynq.SkipRetry) {
			t.Fatalf("invalid task accepted: %v", err)
		}
	}
	if store.finishes != 0 {
		t.Fatal("invalid tasks touched persistence")
	}
}

func TestTranscriptionDedicatedQueue(t *testing.T) {
	cfg := job.TranscriptionWorkerConfig(nil)
	if cfg.Concurrency != 1 || len(cfg.Queues) != 1 || cfg.Queues[transcription.Queue] != 1 {
		t.Fatal("transcription consumes other jobs")
	}
	processing := job.ProcessWorkerConfig(nil)
	if _, exists := processing.Queues[transcription.Queue]; exists {
		t.Fatal("preparation can steal transcription jobs")
	}
}

func TestTranscriptionCancellationPersistsAndCleans(t *testing.T) {
	claim, data, _ := transcriptFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &transcriptStoreStub{claim: claim}
	provider := &transcriptProviderStub{cancel: cancel}
	var logs bytes.Buffer
	handler, root := transcriptHandler(t, store, processLogDownloader{data: data}, &transcriptUploaderStub{}, provider, &logs)
	err := handler.ProcessTask(ctx, asynq.NewTask(transcription.TaskType, []byte(`{"assetId":"asset-test"}`)))
	if err != nil || store.code != "TRANSCRIPTION_INTERRUPTED" || !store.retry || store.pub != nil ||
		store.finishContextError != nil {
		t.Fatalf("cancellation was not durably handled: code=%s err=%v", store.code, err)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("cancelled attempt leaked temporary files")
	}
}
