package migrations

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

type pipelineDownloads struct {
	valid, invalid []byte
	calls          atomic.Int32
}

func (d *pipelineDownloads) Download(ctx context.Context, key, path string, size int64) error {
	d.calls.Add(1)
	if key == "pipeline-transient" {
		return errors.New("private-s3-error-must-not-leak")
	}
	data := d.valid
	if key == "pipeline-rejected" {
		data = d.invalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

type pipelineStore struct {
	job.SQLValidationStore
	claimFailure atomic.Bool
}

func (s *pipelineStore) Claim(ctx context.Context, id, token string, lease time.Duration) (job.ValidationAsset, error) {
	if id == "pipeline-preclaim" && s.claimFailure.Load() {
		return job.ValidationAsset{}, job.ErrValidationDatabase
	}
	return s.SQLValidationStore.Claim(ctx, id, token, lease)
}

// Real disposable PostgreSQL + isolated Redis + real FFprobe. S3 downloads use
// a local synthetic fixture double; this is not a claim of real AWS transfer.
func testValidationPipeline(t *testing.T, parent context.Context, pool *sql.DB) {
	raw := os.Getenv("TEST_ASYNQ_REDIS_URL")
	if raw == "" {
		t.Skip("set TEST_ASYNQ_REDIS_URL to isolated Redis for worker integration")
	}
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip("install FFmpeg and FFprobe for worker integration")
		}
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	fixture := filepath.Join(t.TempDir(), "fixture.mp4")
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=c=black:s=64x48:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "0.25", "-threads", "1", "-c:v", "libx264", "-c:a", "aac", fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("synthetic fixture failed: %v %s", err, output)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REDIS_URL", raw)
	redisConfig, err := config.LoadRedis()
	if err != nil {
		t.Fatal(err)
	}
	client := job.NewClient(redisConfig)
	defer client.Close()
	inspector := asynq.NewInspector(redisConfig.Options)
	defer inspector.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &pipelineStore{SQLValidationStore: job.SQLValidationStore{DB: pool}}
	store.claimFailure.Store(true)
	downloads := &pipelineDownloads{valid: data, invalid: []byte("not a video")}
	tempRoot := filepath.Join(t.TempDir(), "worker")
	handler, err := job.NewValidationHandler(store, downloads, job.ValidationOptions{
		Bucket: "pipeline-bucket", TempDir: tempRoot,
		DownloadTimeout: time.Second, ProbeTimeout: 2 * time.Second, TaskTimeout: 5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetArchiveInspector(inspector)
	// Existing fixtures belong exclusively to this disposable test schema.
	if _, err := pool.ExecContext(ctx, "UPDATE outbox_events SET dispatched_at = clock_timestamp() WHERE dispatched_at IS NULL"); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := pool.QueryRowContext(ctx, "SELECT user_id FROM media_assets WHERE id = 'owned'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	ownedCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: owner})
	dispatcher, err := job.NewDispatcher(pool, client, job.DispatcherOptions{
		PollInterval: time.Millisecond, TransactionTimeout: 2 * time.Second, EnqueueTimeout: time.Second,
		RetryBase: time.Second, RetryMax: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make(map[string]string)
	for _, id := range []string{"pipeline-ready", "pipeline-rejected", "pipeline-transient", "pipeline-preclaim"} {
		size := int64(len(data))
		if id == "pipeline-rejected" {
			size = int64(len(downloads.invalid))
		}
		asset := upload.CompletedAsset{ID: id, Bucket: "pipeline-bucket", Key: id, Filename: "synthetic.mp4", ContentType: "video/mp4", SizeBytes: size}
		if err := (upload.SQLAssets{DB: pool}).SaveCompleted(ownedCtx, asset); err != nil {
			t.Fatal(err)
		}
		result, err := dispatcher.DispatchOne(ctx)
		if err != nil || !result.Delivered {
			t.Fatalf("dispatch: %+v %v", result, err)
		}
		events[id] = result.EventID
		info, err := inspector.GetTaskInfo(job.ValidationQueue, result.EventID)
		if err != nil || info.MaxRetry != 3 || info.Timeout != 20*time.Minute {
			t.Fatalf("worker execution policy not attached: %+v %v", info, err)
		}
	}
	cfg := job.ValidationWorkerConfig(logger)
	cfg.TaskCheckInterval = 10 * time.Millisecond
	cfg.DelayedTaskCheckInterval = 10 * time.Millisecond
	cfg.RetryDelayFunc = func(int, error, *asynq.Task) time.Duration { return 10 * time.Millisecond }
	cfg.ShutdownTimeout = time.Second
	server := asynq.NewServer(redisConfig.Options, cfg)
	mux := asynq.NewServeMux()
	mux.HandleFunc(job.ValidationTaskType, handler.ProcessTask)
	if err := server.Start(mux); err != nil {
		t.Fatal(err)
	}
	defer func() {
		server.Shutdown()
		drainCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := handler.Drain(drainCtx); err != nil {
			t.Error("handler drain failed")
		}
	}()
	await := func(check func() bool) {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("worker integration timed out")
			case <-ticker.C:
			}
		}
	}
	for id, want := range map[string]string{"pipeline-ready": "ready", "pipeline-rejected": "rejected", "pipeline-transient": "validation_failed"} {
		await(func() bool {
			var status string
			return pool.QueryRowContext(ctx, "SELECT status FROM media_assets WHERE id = $1", id).Scan(&status) == nil && status == want
		})
	}
	await(func() bool {
		info, err := inspector.GetTaskInfo(job.ValidationQueue, events["pipeline-preclaim"])
		return err == nil && info.State == asynq.TaskStateArchived
	})
	store.claimFailure.Store(false) // PostgreSQL becomes available again.
	if err := handler.ReconcileArchived(ctx); err != nil {
		t.Fatal(err)
	}
	var status, code string
	var attempts int
	if err := pool.QueryRowContext(ctx, "SELECT status, failure_code, validation_attempts FROM media_assets WHERE id = 'pipeline-preclaim'").Scan(&status, &code, &attempts); err != nil || status != "validation_failed" || code != "VALIDATION_RETRIES_EXHAUSTED" || attempts != 0 {
		t.Fatalf("preclaim failure stranded: status=%s code=%s attempts=%d err=%v", status, code, attempts, err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT validation_attempts FROM media_assets WHERE id = 'pipeline-transient'").Scan(&attempts); err != nil || attempts != 4 {
		t.Fatalf("transient retry count: %d %v", attempts, err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT validation_attempts FROM media_assets WHERE id = 'pipeline-rejected'").Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("permanent rejection retried: %d %v", attempts, err)
	}
	before := downloads.calls.Load()
	_, err = client.EnqueueContext(ctx, asynq.NewTask(job.ValidationTaskType, []byte(`{"assetId":"pipeline-ready"}`)), asynq.Queue(job.ValidationQueue), asynq.TaskID("pipeline-duplicate"))
	if err != nil {
		t.Fatal(err)
	}
	await(func() bool {
		_, err := inspector.GetTaskInfo(job.ValidationQueue, "pipeline-duplicate")
		return errors.Is(err, asynq.ErrTaskNotFound)
	})
	if downloads.calls.Load() != before {
		t.Fatal("terminal duplicate downloaded source again")
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("worker leaked temporary media")
	}
}
