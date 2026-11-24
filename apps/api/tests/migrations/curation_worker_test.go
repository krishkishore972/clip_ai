package migrations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/job"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

type curationObjectGetter struct {
	t     *testing.T
	key   string
	data  []byte
	calls int
}

func (g *curationObjectGetter) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	g.calls++
	if aws.ToString(input.Bucket) != "fixture-bucket" || aws.ToString(input.Key) != g.key {
		g.t.Fatal("untrusted transcript object requested")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(g.data)), ContentLength: aws.Int64(int64(len(g.data)))}, nil
}

func TestCurationWorkerOfflineWorkflow(t *testing.T) {
	ctx, pool := curationStoreDatabase(t)
	if err := curation.CheckSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "empty", "busy", "invalid-output", "checksum"} {
		t.Run(scenario, func(t *testing.T) {
			id := "curation-wire-" + scenario
			data := enqueueCurationFixture(t, ctx, pool, id, scenario == "empty")
			var key string
			if err := pool.QueryRowContext(ctx, `
				SELECT transcript_key FROM media_transcript_files WHERE asset_id = $1
			`, id).Scan(&key); err != nil {
				t.Fatal(err)
			}
			getter := &curationObjectGetter{t: t, key: key, data: data}
			if scenario == "checksum" {
				getter.data = bytes.ReplaceAll(data, []byte("one"), []byte("bad"))
			}
			downloader, err := storage.NewDownloaderWithClient("fixture-bucket", getter)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			token := strings.Repeat("t", 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var input curation.Request
				if r.Header.Get("Authorization") != "Bearer "+token ||
					json.NewDecoder(r.Body).Decode(&input) != nil ||
					len(input.Words) != 3 || input.Words[1].EndMs != 400 ||
					input.Constraints.CandidateCount != 3 {
					t.Error("wrong curation service input")
				}
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "busy":
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"code":"BUSY","message":"secret-provider-body"}`)
				case "invalid-output":
					_, _ = io.WriteString(w, `{"candidates":[{"startWordIndex":0,"endWordIndexExclusive":99,"title":"x","hook":"x","reason":"x"}]}`)
				default:
					_, _ = io.WriteString(w, `{"candidates":[{"startWordIndex":0,"endWordIndexExclusive":2,"title":"First","hook":"Hook","reason":"Reason"},{"startWordIndex":1,"endWordIndexExclusive":3,"title":"Overlap","hook":"Hook","reason":"Reason"}]}`)
				}
			}))
			defer server.Close()
			client, err := curation.NewClient(curation.ClientOptions{URL: server.URL + "/internal/curate", Token: token}, nil)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			handler, err := job.NewCurationHandler(curation.SQLStore{DB: pool}, downloader, client, root, "fixture-bucket", slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer handler.Drain(context.Background())
			payload, _ := json.Marshal(map[string]string{"assetId": id})
			task := asynq.NewTask(curation.TaskType, payload)
			if err := handler.ProcessTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			status, candidates := "ready", 0
			if scenario == "success" {
				candidates = 2
			}
			if scenario == "busy" {
				status = "pending"
			}
			if scenario == "invalid-output" || scenario == "checksum" {
				status = "failed"
			}
			assertCurationState(t, ctx, pool, id, status, 1, candidates)
			wantCalls := 1
			if scenario == "empty" || scenario == "checksum" {
				wantCalls = 0
			}
			if calls != wantCalls || getter.calls != 1 {
				t.Fatal("unverified/empty transcript called service or duplicate I/O")
			}
			if err := handler.ProcessTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			if calls != wantCalls || getter.calls != 1 {
				t.Fatal("replay repeated service/download")
			}
			if scenario == "busy" && countCurationEvents(t, ctx, pool, id) != 2 {
				t.Fatal("retry was not durable")
			}
			if scenario == "success" {
				var end int64
				if err := pool.QueryRowContext(ctx, `
					SELECT end_ms FROM media_curation_candidates WHERE asset_id = $1 AND candidate_index = 0
				`, id).Scan(&end); err != nil || end != 700 {
					t.Fatal("authoritative maximum-end timing not persisted")
				}
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatal("worker leaked transcript temp files")
			}
			for _, secret := range []string{token, key, "secret-provider-body"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("private input in logs")
				}
			}
		})
	}
}

func TestCurationSchemaCheckFailsClosed(t *testing.T) {
	ctx, pool := curationStoreDatabase(t)
	if err := curation.CheckSchema(ctx, nil); !errors.Is(err, curation.ErrConfiguration) {
		t.Fatal("nil DB accepted")
	}
	if _, err := pool.ExecContext(ctx, `
		ALTER TABLE media_curation_candidates RENAME COLUMN start_ms TO missing_start_ms
	`); err != nil {
		t.Fatal(err)
	}
	if err := curation.CheckSchema(ctx, pool); !errors.Is(err, curation.ErrDatabase) {
		t.Fatal("incompatible curation schema accepted")
	}
}
