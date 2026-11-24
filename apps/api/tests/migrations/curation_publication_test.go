package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
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

type curationEnqueuer struct {
	t        *testing.T
	observed bool
}

func (e *curationEnqueuer) EnqueueContext(_ context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	if task.Type() == curation.TaskType {
		e.observed = true
		values := make(map[asynq.OptionType]any)
		for _, opt := range opts {
			values[opt.Type()] = opt.Value()
		}
		taskID, _ := values[asynq.TaskIDOpt].(string)
		var payload map[string]string
		if values[asynq.QueueOpt] != curation.Queue ||
			values[asynq.MaxRetryOpt] != 1 ||
			values[asynq.TimeoutOpt] != 2*time.Minute ||
			taskID == "" ||
			json.Unmarshal(task.Payload(), &payload) != nil ||
			len(payload) != 1 || payload["assetId"] != "publish-success" {
			e.t.Fatalf("curation routing, budget or identifier-only payload drift: %s", task.Payload())
		}
	}
	return &asynq.TaskInfo{}, nil
}

func TestTranscriptPublicationCuration(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	store := transcription.SQLStore{DB: pool}
	for _, scenario := range []string{"success", "missing-preferences", "source-changed", "outbox-failure", "too-large", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			id := "publish-" + scenario
			enqueueTranscriptFile(t, ctx, pool, id)
			claim, err := store.Claim(ctx, id, id+"-claim", time.Minute)
			if err != nil || claim.AssetID != id {
				t.Fatalf("claim: %v", err)
			}
			pub := &transcription.Publication{
				Object: storage.DerivedObject{
					Key:    transcription.TranscriptKey(id, claim.OutputID),
					SHA256: strings.Repeat("c", 64), SizeBytes: 123,
				},
				WordCount: 100, DurationMs: 1000,
			}
			var wantErr error
			switch scenario {
			case "missing-preferences":
				if _, err := pool.ExecContext(ctx, `
					DELETE FROM media_curation_jobs WHERE asset_id = $1
				`, id); err != nil {
					t.Fatal(err)
				}
				wantErr = curation.ErrNoPreferences
			case "source-changed":
				if _, err := pool.ExecContext(ctx, `
					UPDATE media_assets SET duration_ms = duration_ms + 1 WHERE id = $1
				`, id); err != nil {
					t.Fatal(err)
				}
				wantErr = curation.ErrNoTranscript
			case "outbox-failure":
				if _, err := pool.ExecContext(ctx, `
					ALTER TABLE outbox_events
					ADD CONSTRAINT reject_publication_fixture CHECK (
					    task_type <> 'media:curate' OR asset_id <> 'publish-outbox-failure'
					)
				`); err != nil {
					t.Fatal(err)
				}
				wantErr = curation.ErrDatabase
			case "too-large":
				pub.WordCount = curation.MaxTranscriptWords + 1
			case "empty":
				pub.WordCount = 0
			}
			if scenario == "success" {
				var wg sync.WaitGroup
				results := make(chan error, 4)
				for range 4 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						results <- store.Finish(ctx, claim, pub, "", false)
					}()
				}
				wg.Wait()
				close(results)
				successes := 0
				for result := range results {
					if result == nil {
						successes++
					} else if !errors.Is(result, transcription.ErrStale) {
						t.Fatalf("concurrent publication failed: %v", result)
					}
				}
				if successes != 1 {
					t.Fatalf("expected one fenced publication, got %d", successes)
				}
				err = nil
			} else {
				err = store.Finish(ctx, claim, pub, "", false)
			}
			if wantErr != nil {
				if !errors.Is(err, wantErr) {
					t.Fatalf("publication error: got %v, want %v", err, wantErr)
				}
				var intact bool
				if err := pool.QueryRowContext(ctx, `
					SELECT
					    status = 'transcribing'
					    AND claim_token = $2
					    AND output_id = $3
					    AND transcript_sha256 IS NULL
					FROM media_transcript_files
					WHERE asset_id = $1
				`, id, claim.Token, claim.OutputID).Scan(&intact); err != nil || !intact ||
					countCurationEvents(t, ctx, pool, id) != 0 {
					t.Fatal("failed handoff left a partial transcript/event")
				}
				if scenario == "outbox-failure" {
					var unbound bool
					if err := pool.QueryRowContext(ctx, `
						SELECT transcript_sha256 IS NULL
						FROM media_curation_jobs WHERE asset_id = $1
					`, id).Scan(&unbound); err != nil || !unbound {
						t.Fatal("failed publication left a bound curation snapshot")
					}
					if _, err := pool.ExecContext(ctx, `
						ALTER TABLE outbox_events DROP CONSTRAINT reject_publication_fixture
					`); err != nil {
						t.Fatal(err)
					}
					if err := store.Finish(ctx, claim, pub, "", false); err != nil {
						t.Fatalf("same claim could not publish after fault removal: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			state, _ := fileState(t, ctx, pool, id)
			wantEvents := 1
			if scenario == "too-large" {
				wantEvents = 0
			}
			if state != "ready" || countCurationEvents(t, ctx, pool, id) != wantEvents {
				t.Fatal("transcript and curation did not publish together")
			}
			var bound bool
			if err := pool.QueryRowContext(ctx, `
				SELECT CASE WHEN $2 THEN
				    status = 'failed' AND failure_code = 'CURATION_INPUT_TOO_LARGE'
				    AND transcript_sha256 IS NULL
				ELSE
				    status = 'pending' AND transcript_sha256 = $3
				    AND transcript_word_count = $4
				END
				FROM media_curation_jobs WHERE asset_id = $1
			`, id, scenario == "too-large", pub.Object.SHA256, pub.WordCount).Scan(&bound); err != nil || !bound {
				t.Fatalf("curation snapshot/state not published: %v", err)
			}
			if err := store.Finish(ctx, claim, pub, "", false); !errors.Is(err, transcription.ErrStale) {
				t.Fatal("duplicate finish bypassed fencing")
			}
			if countCurationEvents(t, ctx, pool, id) != wantEvents {
				t.Fatal("duplicate finish created work")
			}
		})
	}
	t.Run("failed transcription never queues curation", func(t *testing.T) {
		const id = "publish-provider-failed"
		enqueueTranscriptFile(t, ctx, pool, id)
		claim, err := store.Claim(ctx, id, id+"-claim", time.Minute)
		if err != nil || claim.AssetID != id {
			t.Fatalf("claim: %v", err)
		}
		if err := store.Finish(ctx, claim, nil, "TRANSCRIPTION_PROVIDER_REJECTED", false); err != nil {
			t.Fatal(err)
		}
		state, _ := fileState(t, ctx, pool, id)
		if state != "failed" || countCurationEvents(t, ctx, pool, id) != 0 {
			t.Fatal("failed transcription queued curation")
		}
	})
	enqueuer := &curationEnqueuer{t: t}
	dispatcher, err := job.NewDispatcher(pool, enqueuer, job.DispatcherOptions{
		PollInterval: time.Second, TransactionTimeout: time.Second,
		EnqueueTimeout: 100 * time.Millisecond, RetryBase: time.Second, RetryMax: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30 && !enqueuer.observed; i++ {
		if _, err := dispatcher.DispatchOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !enqueuer.observed {
		t.Fatal("committed curation event was not dispatched")
	}
}
