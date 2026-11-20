package job_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

type processLogStore struct {
	asset     job.ProcessAsset
	claimErr  error
	finishErr error
	outcome   job.ProcessOutcome
}

func (s *processLogStore) Claim(context.Context, string, string, time.Duration) (job.ProcessAsset, error) {
	return s.asset, s.claimErr
}

func (s *processLogStore) Finish(_ context.Context, _ job.ProcessAsset, outcome job.ProcessOutcome) error {
	s.outcome = outcome
	return s.finishErr
}

func (s *processLogStore) Recover(context.Context, int) (int, error) {
	return 0, nil
}

type processLogDownloader struct {
	data []byte
	err  error
}

func (d processLogDownloader) Download(_ context.Context, _, target string, _ int64) error {
	if d.err != nil {
		return d.err
	}
	return os.WriteFile(target, d.data, 0600)
}

type processLogUploader struct{}

func (processLogUploader) UploadDerived(_ context.Context, _, _, _ string, path string) (storage.DerivedObject, error) {
	info, err := os.Stat(path)
	if err != nil {
		return storage.DerivedObject{}, err
	}
	return storage.DerivedObject{Key: "private-derived-key", SizeBytes: info.Size()}, nil
}

func newProcessLogHandler(t *testing.T, store *processLogStore, downloader processLogDownloader, logs *bytes.Buffer) *job.ProcessHandler {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	handler, err := job.NewProcessHandler(store, downloader, processLogUploader{}, job.ProcessOptions{
		Bucket:          "private-bucket",
		TempDir:         root,
		DownloadTimeout: time.Second,
		EncodeTimeout:   20 * time.Second,
		UploadTimeout:   time.Second,
		TaskTimeout:     30 * time.Second,
	}, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestProcessOutcomeLogs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		asset     job.ProcessAsset
		claimErr  error
		finishErr error
		want      string
		wantErr   bool
	}{
		{name: "busy", claimErr: job.ErrProcessBusy, want: `"reason":"claim_busy"`},
		{name: "ineligible", want: `"reason":"not_eligible"`},
		{name: "claim failure", claimErr: errors.New("secret-provider-error"), want: "media processing claim failed", wantErr: true},
		{name: "retry", asset: job.ProcessAsset{ID: "asset-logs", Attempts: 1}, want: `"retry_pending":true`, wantErr: true},
		{name: "exhausted", asset: job.ProcessAsset{ID: "asset-logs", Attempts: job.ProcessAttempts}, want: `"retry_pending":false`, wantErr: true},
		{name: "stale", asset: job.ProcessAsset{ID: "asset-logs", Attempts: 1}, finishErr: job.ErrProcessStale, want: "media processing result discarded"},
		{name: "finish failure", asset: job.ProcessAsset{ID: "asset-logs", Attempts: 1}, finishErr: errors.New("secret-provider-error"), want: "media processing result persistence failed", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			store := &processLogStore{asset: tc.asset, claimErr: tc.claimErr, finishErr: tc.finishErr}
			handler := newProcessLogHandler(t, store, processLogDownloader{err: errors.New("secret-provider-error")}, &logs)
			task := asynq.NewTask(job.ProcessTaskType, []byte(`{"assetId":"asset-logs"}`))
			err := handler.ProcessTask(context.Background(), task)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tc.wantErr)
			}
			for _, want := range []string{"media processing task received", `"asset_id":"asset-logs"`, tc.want} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("missing %q in logs: %s", want, &logs)
				}
			}
			if strings.Contains(logs.String(), "secret-provider-error") || strings.Contains(logs.String(), "media processing completed") {
				t.Fatalf("sensitive error or false success in logs: %s", &logs)
			}
		})
	}
}

func TestProcessInvalidTaskLog(t *testing.T) {
	var logs bytes.Buffer
	handler := newProcessLogHandler(t, &processLogStore{}, processLogDownloader{}, &logs)
	err := handler.ProcessTask(context.Background(), asynq.NewTask(job.ProcessTaskType, []byte("private-invalid-payload")))
	if !errors.Is(err, asynq.SkipRetry) || !strings.Contains(logs.String(), `"code":"INVALID_TASK"`) {
		t.Fatalf("error = %v, logs: %s", err, &logs)
	}
	if strings.Contains(logs.String(), "private-invalid-payload") {
		t.Fatal("invalid task payload leaked")
	}
}

func TestProcessStageLogs(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	source := filepath.Join(t.TempDir(), "fixture.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-n",
		"-f", "lavfi", "-i", "testsrc2=size=160x96:rate=24:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-c:v", "libx264", "-threads", "2", "-c:a", "aac", source,
	}
	if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "success", data: data, want: "media processing completed"},
		{name: "rejection", data: []byte("not video"), want: "media processing rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			store := &processLogStore{asset: job.ProcessAsset{
				ID: "asset-logs", Bucket: "private-bucket", Key: "private-source-key",
				OutputID: "private-output-id", Token: "private-claim-token",
				Attempts: 1, SizeBytes: int64(len(tc.data)),
			}}
			handler := newProcessLogHandler(t, store, processLogDownloader{data: tc.data}, &logs)
			err := handler.ProcessTask(ctx, asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"asset-logs"}`)))
			if err != nil {
				t.Fatalf("process: %v; logs: %s", err, &logs)
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("missing outcome %q: %s", tc.want, &logs)
			}
			if tc.name == "success" {
				if store.outcome.AssetStatus != "ready" || store.outcome.PrepStatus != "ready" {
					t.Fatalf("unexpected persisted outcome: %+v", store.outcome)
				}
				last := -1
				for _, stage := range []string{"download", "validation", "audio_extraction", "audio_upload"} {
					for _, event := range []string{"started", "completed"} {
						want := `"msg":"media processing stage ` + event + `","asset_id":"asset-logs","task_type":"media:validate","attempt":1,"stage":"` + stage + `"`
						position := strings.Index(logs.String(), want)
						if position <= last {
							t.Fatalf("missing/out-of-order stage %q: %s", want, &logs)
						}
						last = position
					}
				}
			} else if strings.Contains(logs.String(), "media processing completed") {
				t.Fatal("rejected media logged as successful")
			}
			for _, secret := range []string{"private-bucket", "private-source-key", "private-output-id", "private-claim-token", "private-derived-key", source} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("logs leaked %q", secret)
				}
			}
		})
	}
}
