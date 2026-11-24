package migrations_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/transcription"
	"github.com/krishkishore972/clip_ai/migrations"
	"github.com/pressly/goose/v3"
)

func transcriptFileDatabase(t *testing.T) (context.Context, *sql.DB, *goose.Provider) {
	t.Helper()
	ctx, pool := processTestDatabase(t)
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	// processTestDatabase already reached fixtureSchemaVersion; this call only
	// keeps the transcript fixtures explicit about the schema they need.
	if _, err := provider.UpTo(ctx, fixtureSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := transcription.CheckFileSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, pool, provider
}

func enqueueTranscriptFile(t *testing.T, ctx context.Context, pool *sql.DB, id string) {
	t.Helper()
	asset := claimProcessFixture(t, ctx, pool, id)
	if err := (job.SQLProcessStore{DB: pool, TranscriptionLanguage: "multi"}).Finish(ctx, asset, readyProcessOutcome(asset)); err != nil {
		t.Fatal(err)
	}
}

func fileState(t *testing.T, ctx context.Context, pool *sql.DB, id string) (string, int) {
	t.Helper()
	var state string
	var attempts int
	if err := pool.QueryRowContext(ctx, `
		SELECT status, attempts FROM media_transcript_files WHERE asset_id = $1
	`, id).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	return state, attempts
}

func dueTranscript(t *testing.T, ctx context.Context, pool *sql.DB, id string) {
	t.Helper()
	if _, err := pool.ExecContext(ctx, `
		UPDATE media_transcript_files
		SET next_attempt_at = clock_timestamp() - INTERVAL '1 second'
		WHERE asset_id = $1
	`, id); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptFileLifecycle(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	store := transcription.SQLStore{DB: pool}

	t.Run("enqueue and idempotency", func(t *testing.T) {
		enqueueTranscriptFile(t, ctx, pool, "file-enqueue")
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := transcription.InsertFileJob(ctx, tx, "file-enqueue", "ml"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM outbox_events
			WHERE asset_id = 'file-enqueue' AND task_type = $1
			    AND queue_name = $2 AND payload = '{"assetId":"file-enqueue"}'::jsonb
		`, transcription.TaskType, transcription.Queue).Scan(&count); err != nil || count != 1 {
			t.Fatalf("enqueue not identifier-only/idempotent: %d %v", count, err)
		}
		claim, err := store.Claim(ctx, "file-enqueue", "claim-one", time.Minute)
		if err != nil || claim.Attempts != 1 || claim.Language != "multi" || claim.AudioStartMs != -7 {
			t.Fatalf("snapshot claim: %+v %v", claim, err)
		}
		if _, err := store.Claim(ctx, "file-enqueue", "claim-two", time.Minute); !errors.Is(err, transcription.ErrBusy) {
			t.Fatalf("duplicate claim accepted: %v", err)
		}
		pub := &transcription.Publication{
			Object:    storage.DerivedObject{Key: transcription.TranscriptKey(claim.AssetID, claim.OutputID), SHA256: strings.Repeat("c", 64), SizeBytes: 123},
			WordCount: 1, DurationMs: 1000,
		}
		stale := claim
		stale.Token = "stale"
		if err := store.Finish(ctx, stale, pub, "", false); !errors.Is(err, transcription.ErrStale) {
			t.Fatalf("stale finish: %v", err)
		}
		if err := store.Finish(ctx, claim, pub, "", false); err != nil {
			t.Fatal(err)
		}
		state, attempts := fileState(t, ctx, pool, claim.AssetID)
		if state != "ready" || attempts != 1 {
			t.Fatal("ready publication failed")
		}
		if next, err := store.Claim(ctx, claim.AssetID, "terminal", time.Minute); err != nil || next.AssetID != "" {
			t.Fatal("terminal transcript reclaimed")
		}
		if _, err := provider.Down(ctx); err == nil {
			t.Fatal("nonempty migration rollback allowed")
		}
	})

	t.Run("retry delay and exhaustion", func(t *testing.T) {
		enqueueTranscriptFile(t, ctx, pool, "file-retry")
		first, err := store.Claim(ctx, "file-retry", "retry-one", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, first, nil, "TRANSCRIPTION_TIMEOUT", true); err != nil {
			t.Fatal(err)
		}
		if early, err := store.Claim(ctx, first.AssetID, "too-early", time.Minute); err != nil || early.AssetID != "" {
			t.Fatal("duplicate delivery bypassed retry backoff")
		}
		dueTranscript(t, ctx, pool, first.AssetID)
		second, err := store.Claim(ctx, first.AssetID, "retry-two", time.Minute)
		if err != nil || second.Attempts != 2 {
			t.Fatalf("second claim: %+v %v", second, err)
		}
		if err := store.Finish(ctx, second, nil, "TRANSCRIPTION_TIMEOUT", true); err != nil {
			t.Fatal(err)
		}
		state, attempts := fileState(t, ctx, pool, first.AssetID)
		if state != "failed" || attempts != 2 {
			t.Fatal("attempt budget exceeded")
		}
		var count int
		if err := pool.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM outbox_events
			WHERE asset_id = $1 AND dedupe_key LIKE '%:retry:%'
		`, first.AssetID).Scan(&count); err != nil || count != 1 {
			t.Fatal("durable retry count drift")
		}
	})

	t.Run("expired and preclaim recovery", func(t *testing.T) {
		enqueueTranscriptFile(t, ctx, pool, "file-expired")
		claim, err := store.Claim(ctx, "file-expired", "expired-token", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_transcript_files SET lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE asset_id = 'file-expired'
		`); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatalf("lease recovery: %d %v", n, err)
		}
		if err := store.Finish(ctx, claim, nil, "TRANSCRIPTION_TIMEOUT", true); !errors.Is(err, transcription.ErrStale) {
			t.Fatal("recovered claim can still publish")
		}
		dueTranscript(t, ctx, pool, claim.AssetID)
		if next, err := store.Claim(ctx, claim.AssetID, "expired-second", time.Minute); err != nil || next.Attempts != 2 {
			t.Fatal("second recoverable attempt not claimed")
		}
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_transcript_files SET lease_until = clock_timestamp() - INTERVAL '1 second'
			WHERE asset_id = 'file-expired'
		`); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatalf("exhausted recovery: %d %v", n, err)
		}
		if state, attempts := fileState(t, ctx, pool, claim.AssetID); state != "failed" || attempts != 2 {
			t.Fatal("expired claims exceeded durable attempt budget")
		}
		enqueueTranscriptFile(t, ctx, pool, "file-preclaim")
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_transcript_files SET updated_at = clock_timestamp() - INTERVAL '31 minutes'
			WHERE asset_id = 'file-preclaim'
		`); err != nil {
			t.Fatal(err)
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 1 {
			t.Fatalf("preclaim repair: %d %v", n, err)
		}
		state, attempts := fileState(t, ctx, pool, "file-preclaim")
		if state != "pending" || attempts != 0 {
			t.Fatal("repair consumed a paid attempt")
		}
		if n, err := store.Recover(ctx, 100); err != nil || n != 0 {
			t.Fatal("repair was not deduplicated")
		}
	})

	t.Run("preparation transaction rollback", func(t *testing.T) {
		asset := claimProcessFixture(t, ctx, pool, "file-rollback")
		if _, err := pool.ExecContext(ctx, `
			CREATE FUNCTION reject_transcript_event() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    IF NEW.asset_id = 'file-rollback' AND NEW.task_type = 'media:transcribe-file' THEN
			        RAISE EXCEPTION 'fixture failure';
			    END IF;
			    RETURN NEW;
			END;
			$$;
			CREATE TRIGGER reject_transcript_event BEFORE INSERT ON outbox_events
			FOR EACH ROW EXECUTE FUNCTION reject_transcript_event()
		`); err != nil {
			t.Fatal(err)
		}
		err := (job.SQLProcessStore{DB: pool, TranscriptionLanguage: "multi"}).Finish(ctx, asset, readyProcessOutcome(asset))
		if err == nil {
			t.Fatal("outbox failure did not abort publication")
		}
		assertProcessState(t, ctx, pool, asset.ID, "validating", "preparing", 1)
		var count int
		if err := pool.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM media_transcript_files WHERE asset_id = $1
		`, asset.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial transcript publication survived")
		}
	})

	t.Run("changed preparation cannot reach provider", func(t *testing.T) {
		enqueueTranscriptFile(t, ctx, pool, "file-changed")
		if _, err := pool.ExecContext(ctx, `
			UPDATE media_preparations SET audio_sha256 = $2 WHERE asset_id = $1
		`, "file-changed", strings.Repeat("d", 64)); err != nil {
			t.Fatal(err)
		}
		claim, err := store.Claim(ctx, "file-changed", "changed-claim", time.Minute)
		if err != nil || claim.AssetID != "" {
			t.Fatal("changed source was claimed")
		}
		state, _ := fileState(t, ctx, pool, "file-changed")
		if state != "failed" {
			t.Fatal("changed source left stuck work")
		}
	})
}

func TestTranscriptFileFeatureDisabled(t *testing.T) {
	ctx, pool := processTestDatabase(t)
	asset := claimProcessFixture(t, ctx, pool, "file-disabled")
	if err := (job.SQLProcessStore{DB: pool}).Finish(ctx, asset, readyProcessOutcome(asset)); err != nil {
		t.Fatal("feature-disabled preparation must work without migration 00008")
	}
	var count int
	if err := pool.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM outbox_events WHERE task_type = $1
	`, transcription.TaskType).Scan(&count); err != nil || count != 0 {
		t.Fatal("disabled feature created paid work")
	}
}

func TestTranscriptFileMigrationEmptyRollback(t *testing.T) {
	ctx, _, provider := transcriptFileDatabase(t)
	// Drop the empty curation schema first; this test targets migration 8.
	if result, err := provider.Down(ctx); err != nil || result.Source.Version != 9 {
		t.Fatalf("empty curation rollback failed: %v", err)
	}
	result, err := provider.Down(ctx)
	if err != nil || result.Source.Version != 8 {
		t.Fatalf("empty rollback failed: %v", err)
	}
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if up, err := provider.UpTo(ctx, 9); err != nil || len(up) != 0 {
		t.Fatal("repeat up not a no-op")
	}
}

type transcriptEnqueuer struct {
	t        *testing.T
	observed bool
}

func (e *transcriptEnqueuer) EnqueueContext(_ context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	if task.Type() == transcription.TaskType {
		e.observed = true
		values := make(map[asynq.OptionType]interface{})
		for _, opt := range opts {
			values[opt.Type()] = opt.Value()
		}
		if values[asynq.QueueOpt] != transcription.Queue || values[asynq.MaxRetryOpt] != 1 ||
			values[asynq.TimeoutOpt] != job.TranscriptionTaskTimeout || values[asynq.TaskIDOpt] == "" {
			e.t.Fatal("transcription dispatcher routing/budget drift")
		}
	}
	return &asynq.TaskInfo{}, nil
}

func TestTranscriptFileDispatcherRouting(t *testing.T) {
	ctx, pool, _ := transcriptFileDatabase(t)
	enqueueTranscriptFile(t, ctx, pool, "file-dispatch")
	enqueuer := &transcriptEnqueuer{t: t}
	dispatcher, err := job.NewDispatcher(pool, enqueuer, job.DispatcherOptions{
		PollInterval: time.Second, TransactionTimeout: time.Second,
		EnqueueTimeout: 100 * time.Millisecond, RetryBase: time.Second, RetryMax: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5 && !enqueuer.observed; i++ {
		if _, err := dispatcher.DispatchOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !enqueuer.observed {
		t.Fatal("transcription event not dispatched")
	}
}

type transcriptObjectStub struct {
	t                 *testing.T
	audio, transcript []byte
}

func (s *transcriptObjectStub) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if aws.ToString(input.Bucket) != "fixture-bucket" || !strings.HasSuffix(aws.ToString(input.Key), "/audio.wav") {
		s.t.Fatal("untrusted audio source")
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(s.audio)), ContentLength: aws.Int64(int64(len(s.audio))),
	}, nil
}

func (s *transcriptObjectStub) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if aws.ToString(input.Bucket) != "fixture-bucket" || !strings.HasSuffix(aws.ToString(input.Key), "/transcript.json") ||
		aws.ToString(input.IfNoneMatch) != "*" || input.ACL != "" {
		s.t.Fatal("unsafe transcript upload")
	}
	var err error
	s.transcript, err = io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	return &s3.PutObjectOutput{ChecksumSHA256: input.ChecksumSHA256}, nil
}

type transcriptHTTPFunc func(*http.Request) (*http.Response, error)

func (f transcriptHTTPFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTranscriptFileOfflineEndToEnd(t *testing.T) {
	ctx, pool, _ := transcriptFileDatabase(t)
	data := bytes.Repeat([]byte{1}, 32044)
	hash := sha256.Sum256(data)
	asset := claimProcessFixture(t, ctx, pool, "file-end-to-end")
	outcome := readyProcessOutcome(asset)
	outcome.Artifacts.AudioSHA256 = hex.EncodeToString(hash[:])
	if err := (job.SQLProcessStore{DB: pool, TranscriptionLanguage: "multi"}).Finish(ctx, asset, outcome); err != nil {
		t.Fatal(err)
	}
	objects := &transcriptObjectStub{t: t, audio: data}
	downloader, err := storage.NewDownloaderWithClient("fixture-bucket", objects)
	if err != nil {
		t.Fatal(err)
	}
	uploader, err := storage.NewDerivedUploaderWithClient(config.S3{Region: "offline", Bucket: "fixture-bucket"}, objects)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	provider, err := transcription.NewClient("offline", &http.Client{Transport: transcriptHTTPFunc(func(r *http.Request) (*http.Response, error) {
		providerCalls++
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, data) || r.URL.Query().Get("language") != "multi" {
			t.Fatal("SDK input drift")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"metadata":{"duration":1},
				"results":{"channels":[{"alternatives":[{
					"transcript":"నమస్కారం", "languages":["te"],
					"words":[{"word":"నమస్కారం","start":0.1,"end":0.5,"confidence":0.9,"language":"te"}]
				}]}]}
			}`)),
		}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler, err := job.NewTranscriptionHandler(transcription.SQLStore{DB: pool}, downloader, uploader, provider, root, "fixture-bucket", slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	task := asynq.NewTask(transcription.TaskType, []byte(`{"assetId":"file-end-to-end"}`))
	if err := handler.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := handler.ProcessTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	state, attempts := fileState(t, ctx, pool, asset.ID)
	if state != "ready" || attempts != 1 || providerCalls != 1 {
		t.Fatal("duplicate event repeated paid work")
	}
	var doc transcription.Document
	if err := json.Unmarshal(objects.transcript, &doc); err != nil ||
		doc.Transcript != "నమస్కారం" || doc.Words[0].StartMs != 93 || doc.Words[0].Language != "te" {
		t.Fatal("private normalized transcript was not uploaded")
	}
	var key, storedHash string
	var size int64
	if err := pool.QueryRowContext(ctx, `
		SELECT transcript_key, transcript_sha256, transcript_size_bytes
		FROM media_transcript_files WHERE asset_id = $1
	`, asset.ID).Scan(&key, &storedHash, &size); err != nil {
		t.Fatal(err)
	}
	objectHash := sha256.Sum256(objects.transcript)
	if !strings.HasSuffix(key, "/transcript.json") || storedHash != hex.EncodeToString(objectHash[:]) || size != int64(len(objects.transcript)) {
		t.Fatal("database did not publish the exact uploaded object")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("temporary audio/transcript files leaked")
	}
	if strings.Contains(logs.String(), "నమస్కారం") || strings.Contains(logs.String(), "audio.wav") {
		t.Fatal("user content/storage data leaked to logs")
	}
}
