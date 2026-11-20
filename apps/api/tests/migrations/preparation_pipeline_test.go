package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/handler"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

type preparationPipelineUploads struct {
	db      *sql.DB
	mu      sync.Mutex
	objects map[string][]byte
}

func (u *preparationPipelineUploads) UploadDerived(ctx context.Context, assetID, outputID, kind, path string) (storage.DerivedObject, error) {
	if err := ctx.Err(); err != nil {
		return storage.DerivedObject{}, err
	}
	var token string
	if err := u.db.QueryRowContext(ctx, `SELECT claim_token FROM media_preparations WHERE asset_id = $1`, assetID).Scan(&token); err != nil {
		return storage.DerivedObject{}, err
	}
	if outputID == token {
		return storage.DerivedObject{}, errors.New("output namespace leaked fencing token")
	}
	if kind != "audio" {
		return storage.DerivedObject{}, errors.New("unexpected output kind")
	}
	filename := "audio.wav"
	data, err := os.ReadFile(path)
	if err != nil {
		return storage.DerivedObject{}, err
	}
	key := "dev-uploads/" + assetID + "/derived/v1/" + outputID + "/" + filename
	sum := sha256.Sum256(data)
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, exists := u.objects[key]; exists {
		return storage.DerivedObject{}, errors.New("immutable output overwritten")
	}
	u.objects[key] = data
	return storage.DerivedObject{Key: key, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// Join real SQL upload/validation/preparation, native Redis queues, default
// FFprobe/FFmpeg execution and owner HTTP projection. Only S3 I/O is doubled.
func testPreparationPipeline(t *testing.T, parent context.Context, pool *sql.DB) {
	raw := os.Getenv("TEST_ASYNQ_REDIS_URL")
	if raw == "" {
		t.Skip("set TEST_ASYNQ_REDIS_URL to isolated Redis for joined preparation integration")
	}
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip("install FFmpeg/FFprobe for joined preparation integration")
		}
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	fixture := filepath.Join(t.TempDir(), "source.mp4")
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=c=blue:s=96x64:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "1", "-threads", "1", "-c:v", "libx264", "-c:a", "aac", fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("joined fixture: %v %s", err, output)
	}
	source, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256(source)
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
	downloads := &pipelineDownloads{valid: source}
	uploads := &preparationPipelineUploads{db: pool, objects: make(map[string][]byte)}
	validationRoot := filepath.Join(t.TempDir(), "validation")
	preparationRoot := filepath.Join(t.TempDir(), "preparation")
	validationHandler, err := job.NewValidationHandler(job.SQLValidationStore{DB: pool, PrepareValidated: true}, downloads, job.ValidationOptions{
		Bucket: "joined-pipeline-bucket", TempDir: validationRoot, DownloadTimeout: 2 * time.Second, ProbeTimeout: 3 * time.Second, TaskTimeout: 10 * time.Second,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	preparationStore := preparation.SQLStore{DB: pool}
	preparationHandler, err := job.NewPreparationHandler(preparationStore, downloads, uploads, job.PreparationOptions{
		Bucket: "joined-pipeline-bucket", TempDir: preparationRoot,
		DownloadTimeout: 2 * time.Second, EncodeTimeout: 10 * time.Second, UploadTimeout: 2 * time.Second, TaskTimeout: 20 * time.Second,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	// Everything preceding this test belongs to the same disposable schema.
	// Do not dispatch its synthetic pending fixtures into this joined workflow.
	if _, err := pool.ExecContext(ctx, `
  UPDATE outbox_events
  SET dispatched_at = clock_timestamp()
  WHERE dispatched_at IS NULL
 `); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := pool.QueryRowContext(ctx, "SELECT user_id FROM media_assets WHERE id = 'owned'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	owned := auth.WithPrincipal(ctx, auth.Principal{UserID: owner})
	const assetID = "joined-preparation-ready"
	const originalKey = "joined-original-immutable"
	if err := (upload.SQLAssets{DB: pool}).SaveCompleted(owned, upload.CompletedAsset{
		ID: assetID, Bucket: "joined-pipeline-bucket", Key: originalKey, Filename: "synthetic.mp4", ContentType: "video/mp4", SizeBytes: int64(len(source)),
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := job.NewDispatcher(pool, client, job.DispatcherOptions{
		PollInterval: time.Millisecond, TransactionTimeout: 2 * time.Second, EnqueueTimeout: time.Second, RetryBase: time.Second, RetryMax: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(queue string, retry int, timeout time.Duration) string {
		t.Helper()
		result, err := dispatcher.DispatchOne(ctx)
		if err != nil || !result.Delivered {
			t.Fatalf("dispatch %s: %+v %v", queue, result, err)
		}
		info, err := inspector.GetTaskInfo(queue, result.EventID)
		if err != nil || info.MaxRetry != retry || info.Timeout != timeout {
			t.Fatalf("native queue policy %s: %+v %v", queue, info, err)
		}
		return result.EventID
	}
	validationEvent := dispatch(job.ValidationQueue, 3, 20*time.Minute)
	validationConfig := job.ValidationWorkerConfig(logger)
	validationConfig.TaskCheckInterval = 10 * time.Millisecond
	validationConfig.ShutdownTimeout = time.Second
	validationServer := asynq.NewServer(redisConfig.Options, validationConfig)
	validationMux := asynq.NewServeMux()
	validationMux.HandleFunc(job.ValidationTaskType, validationHandler.ProcessTask)
	if err := validationServer.Start(validationMux); err != nil {
		t.Fatal(err)
	}
	defer func() {
		validationServer.Shutdown()
		drain, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := validationHandler.Drain(drain); err != nil {
			t.Error("joined validation handler drain failed")
		}
	}()
	await := func(label string, check func() bool) {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatalf("joined pipeline timed out awaiting %s", label)
			case <-ticker.C:
			}
		}
	}
	await("validation READY and automatic preparation outbox", func() bool {
		var count int
		err := pool.QueryRowContext(ctx, `
   SELECT COUNT(*)
   FROM media_assets a
   JOIN media_preparations p ON p.asset_id = a.id
   JOIN outbox_events e ON e.asset_id = a.id
   WHERE a.id = $1
    AND a.status = 'ready'
    AND p.status = 'pending'
    AND e.dedupe_key = 'prepare:' || a.id || ':v1'
    AND e.dispatched_at IS NULL
  `, assetID).Scan(&count)
		return err == nil && count == 1
	})
	httpMux := http.NewServeMux()
	handler.NewMediaPreparationHandler(preparationStore, true, logger).Register(httpMux)
	request := func(principal, path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/media/"+assetID+path, nil).WithContext(ctx)
		if principal != "" {
			request = request.WithContext(auth.WithPrincipal(ctx, auth.Principal{UserID: principal}))
		}
		result := httptest.NewRecorder()
		httpMux.ServeHTTP(result, request)
		if result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("joined HTTP response lacks no-store")
		}
		return result
	}
	preparationEvent := dispatch(preparation.Queue, 1, 90*time.Minute)
	preparationConfig := job.PreparationWorkerConfig(logger)
	preparationConfig.TaskCheckInterval = 10 * time.Millisecond
	preparationConfig.ShutdownTimeout = time.Second
	preparationServer := asynq.NewServer(redisConfig.Options, preparationConfig)
	preparationMux := asynq.NewServeMux()
	preparationMux.HandleFunc(preparation.TaskType, preparationHandler.ProcessTask)
	if err := preparationServer.Start(preparationMux); err != nil {
		t.Fatal(err)
	}
	defer func() {
		preparationServer.Shutdown()
		drain, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := preparationHandler.Drain(drain); err != nil {
			t.Error("joined preparation handler drain failed")
		}
	}()
	await("preparation READY", func() bool {
		status, err := preparationStore.GetStatus(ctx, owner, assetID)
		if err == nil && status.Status == "failed" {
			t.Fatalf("real encoder/worker contract failed: %+v", status)
		}
		return err == nil && status.Status == "ready" && status.Attempts == 1
	})
	// Queue acknowledgement follows deferred attempt-directory cleanup.
	for queue, event := range map[string]string{job.ValidationQueue: validationEvent, preparation.Queue: preparationEvent} {
		await("acknowledged "+queue, func() bool {
			_, err := inspector.GetTaskInfo(queue, event)
			return errors.Is(err, asynq.ErrTaskNotFound)
		})
	}
	for _, root := range []string{validationRoot, preparationRoot} {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("joined worker leaked temporary output: %v", err)
		}
	}
	var audioKey, audioSHA, sourceSHA string
	var audioSize int64
	if err := pool.QueryRowContext(ctx, `
  SELECT
   audio_key, audio_sha256, source_sha256,
   audio_size_bytes
  FROM media_preparations
  WHERE asset_id = $1 AND status = 'ready'
 `, assetID).Scan(&audioKey, &audioSHA, &sourceSHA, &audioSize); err != nil {
		t.Fatal(err)
	}
	uploads.mu.Lock()
	captured := make(map[string][]byte, len(uploads.objects))
	for key, value := range uploads.objects {
		captured[key] = value
	}
	uploads.mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("expected one immutable object, got %d", len(captured))
	}
	for _, object := range []struct {
		key, hash string
		size      int64
	}{{audioKey, audioSHA, audioSize}} {
		data, ok := captured[object.key]
		sum := sha256.Sum256(data)
		if !ok || int64(len(data)) != object.size || hex.EncodeToString(sum[:]) != object.hash || object.key == originalKey {
			t.Fatalf("published derivative mismatch: %s", object.key)
		}
	}
	// Independently inspect the exact uploaded bytes, not just encoder-returned
	// metadata: WAV is PCM16/16k/mono.
	for _, object := range []struct{ key, name string }{{audioKey, "audio.wav"}} {
		path := filepath.Join(t.TempDir(), object.name)
		if err := os.WriteFile(path, captured[object.key], 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
			"stream=codec_type,codec_name,width,height,avg_frame_rate,sample_rate,channels", "-of", "json", path)
		output, err := command.Output()
		if err != nil {
			t.Fatalf("uploaded %s independent probe failed: %v", object.name, err)
		}
		var probe struct {
			Streams []struct {
				Type       string `json:"codec_type"`
				Codec      string `json:"codec_name"`
				Width      int    `json:"width"`
				Height     int    `json:"height"`
				FPS        string `json:"avg_frame_rate"`
				SampleRate string `json:"sample_rate"`
				Channels   int    `json:"channels"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(output, &probe); err != nil {
			t.Fatal(err)
		}
		audio := false
		for _, stream := range probe.Streams {
			if stream.Type == "audio" {
				audio = stream.Codec == "pcm_s16le" && stream.SampleRate == "16000" && stream.Channels == 1
			}
		}
		if !audio || len(probe.Streams) != 1 {
			t.Fatalf("uploaded %s has incorrect ASR metadata: %s", object.name, output)
		}
	}
	if sourceSHA != hex.EncodeToString(sourceHash[:]) {
		t.Fatal("persisted source checksum changed")
	}
	after, err := os.ReadFile(fixture)
	if err != nil || sha256.Sum256(after) != sourceHash {
		t.Fatal("original fixture mutated")
	}
	var sourceStatus, sourceKey string
	if err := pool.QueryRowContext(ctx, "SELECT status, storage_key FROM media_assets WHERE id = $1", assetID).Scan(&sourceStatus, &sourceKey); err != nil || sourceStatus != "ready" || sourceKey != originalKey {
		t.Fatal("preparation changed validated source")
	}
	statusResult := request(owner, "/preparation")
	var status preparation.Status
	if err := json.Unmarshal(statusResult.Body.Bytes(), &status); err != nil || statusResult.Code != 200 || status.Status != "ready" {
		t.Fatalf("HTTP joined status: %d %s", statusResult.Code, statusResult.Body.String())
	}
	for _, private := range []string{audioKey, originalKey, sourceSHA, "signature="} {
		if strings.Contains(statusResult.Body.String(), private) {
			t.Fatalf("status leaked private field %q", private)
		}
	}
	for _, path := range []string{"/preparation"} {
		if result := request("", path); result.Code != 401 {
			t.Fatalf("anonymous joined HTTP %s: %d", path, result.Code)
		}
		if result := request("wrong-owner", path); result.Code != 404 {
			t.Fatalf("cross-owner joined HTTP %s: %d", path, result.Code)
		}
	}
	if downloads.calls.Load() != 2 {
		t.Fatalf("unexpected source download count: %d", downloads.calls.Load())
	}
	t.Log(fmt.Sprintf("joined validation/preparation completed with %d private derivative objects", len(captured)))
}
