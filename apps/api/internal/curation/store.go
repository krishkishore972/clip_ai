package curation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	ClaimLease      = 3 * time.Minute
	RetryDelay      = time.Minute
	PendingRecovery = 5 * time.Minute
	MaxAttempts     = 2
	databaseTimeout = 5 * time.Second
)

var (
	ErrStale    = errors.New("curation: stale claim")
	ErrBusy     = errors.New("curation: claim busy")
	ErrResult   = errors.New("curation: invalid result")
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Claim snapshots server-owned identity, input and preferences. No caller may
// replace its fields between Claim, ValidateResult and Finish.
type Claim struct {
	AssetID, UserID, Token                            string
	TranscriptBucket, TranscriptKey, TranscriptSHA256 string
	TranscriptSizeBytes, SourceDurationMs             int64
	TranscriptWordCount, CandidateCount               int
	MinDurationMs, MaxDurationMs                      int64
	Model, PromptVersion                              string
	Attempts                                          int
}

func (c Claim) valid() bool {
	prefix := "dev-uploads/" + c.AssetID + "/derived/v1/"
	part := strings.TrimSuffix(strings.TrimPrefix(c.TranscriptKey, prefix), "/transcript.json")
	return assetIdentifier.MatchString(c.AssetID) && c.UserID != "" &&
		assetIdentifier.MatchString(c.Token) && c.Attempts >= 1 && c.Attempts <= MaxAttempts &&
		c.TranscriptBucket != "" && strings.HasPrefix(c.TranscriptKey, prefix) &&
		strings.HasSuffix(c.TranscriptKey, "/transcript.json") && assetIdentifier.MatchString(part) &&
		hashPattern.MatchString(c.TranscriptSHA256) &&
		c.TranscriptSizeBytes > 0 && c.TranscriptSizeBytes <= 16*1024*1024 &&
		c.TranscriptWordCount >= 0 && c.TranscriptWordCount <= MaxTranscriptWords &&
		c.SourceDurationMs > 0 && c.SourceDurationMs <= 7_200_000 &&
		c.CandidateCount >= 1 && c.CandidateCount <= 5 &&
		c.MinDurationMs >= 1 && c.MaxDurationMs >= c.MinDurationMs && c.MaxDurationMs <= 60_000 &&
		c.Model == Model && c.PromptVersion == PromptVersion
}

type SQLStore struct{ DB *sql.DB }

type lockedJob struct {
	claim               Claim
	status              string
	due, live, eligible bool
	lease               sql.NullTime
}

// lockJob shares the parent asset first so owner/validation changes cannot race
// publication. It then locks transcript before curation, matching the publisher.
// With skip set, recovery skips curation rows held by another operation.
func lockJob(ctx context.Context, tx *sql.Tx, id string, skip bool) (lockedJob, error) {
	var assetID string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM media_assets WHERE id = $1 FOR SHARE
	`, id).Scan(&assetID)
	if errors.Is(err, sql.ErrNoRows) {
		return lockedJob{}, nil
	}
	if err != nil {
		return lockedJob{}, storeError(ctx)
	}
	var source transcriptSnapshot
	var owner string
	var eligible bool
	err = tx.QueryRowContext(ctx, `
		SELECT
		    COALESCE(a.user_id, ''),
		    t.storage_bucket,
		    COALESCE(t.transcript_key, ''),
		    COALESCE(t.transcript_sha256, ''),
		    COALESCE(t.transcript_size_bytes, 0),
		    COALESCE(t.word_count, 0),
		    t.source_duration_ms,
		    COALESCE(
		        t.status = 'ready' AND a.status = 'ready'
		        AND a.user_id IS NOT NULL AND a.duration_ms = t.source_duration_ms,
		        FALSE
		    )
		FROM media_transcript_files t
		JOIN media_assets a ON a.id = t.asset_id
		WHERE t.asset_id = $1
		FOR UPDATE OF t
	`, id).Scan(&owner, &source.bucket, &source.key, &source.sha256,
		&source.sizeBytes, &source.wordCount, &source.sourceDurationMs, &eligible)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return lockedJob{}, storeError(ctx)
	}
	query := `
		SELECT
		    asset_id,
		    COALESCE(claim_token, ''),
		    COALESCE(transcript_bucket, ''),
		    COALESCE(transcript_key, ''),
		    COALESCE(transcript_sha256, ''),
		    COALESCE(transcript_size_bytes, 0),
		    COALESCE(transcript_word_count, 0),
		    COALESCE(source_duration_ms, 0),
		    candidate_count,
		    min_duration_ms,
		    max_duration_ms,
		    model,
		    prompt_version,
		    attempts,
		    status,
		    next_attempt_at <= clock_timestamp(),
		    COALESCE(lease_until > clock_timestamp(), FALSE),
		    lease_until
		FROM media_curation_jobs
		WHERE asset_id = $1
		FOR UPDATE`
	if skip {
		query += " SKIP LOCKED"
	}
	var j lockedJob
	c := &j.claim
	err = tx.QueryRowContext(ctx, query, id).Scan(
		&c.AssetID, &c.Token, &c.TranscriptBucket, &c.TranscriptKey, &c.TranscriptSHA256,
		&c.TranscriptSizeBytes, &c.TranscriptWordCount, &c.SourceDurationMs,
		&c.CandidateCount, &c.MinDurationMs, &c.MaxDurationMs, &c.Model, &c.PromptVersion,
		&c.Attempts, &j.status, &j.due, &j.live, &j.lease,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return lockedJob{}, nil
	}
	if err != nil {
		return lockedJob{}, storeError(ctx)
	}
	c.UserID = owner
	j.eligible = eligible && c.TranscriptBucket == source.bucket &&
		c.TranscriptKey == source.key && c.TranscriptSHA256 == source.sha256 &&
		c.TranscriptSizeBytes == source.sizeBytes && c.TranscriptWordCount == source.wordCount &&
		c.SourceDurationMs == source.sourceDurationMs
	return j, nil
}

// Claim never consumes an attempt while waiting for transcription or retry due
// time. A live duplicate returns ErrBusy; expired running claims need Recover.
func (s SQLStore) Claim(ctx context.Context, id, token string) (Claim, error) {
	if s.DB == nil || !assetIdentifier.MatchString(id) || !assetIdentifier.MatchString(token) {
		return Claim{}, ErrConfiguration
	}
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return Claim{}, storeError(op)
	}
	defer tx.Rollback()
	j, err := lockJob(op, tx, id, false)
	if err != nil {
		return Claim{}, err
	}
	if j.status == "running" {
		return Claim{}, ErrBusy
	}
	if j.status != "pending" || !j.due || j.claim.TranscriptSHA256 == "" {
		return Claim{}, nil
	}
	c := j.claim
	c.Token, c.Attempts = token, c.Attempts+1
	code := ""
	if j.claim.Attempts >= MaxAttempts {
		code = "CURATION_RETRIES_EXHAUSTED"
	} else if !j.eligible || !c.valid() {
		code = "CURATION_SOURCE_CHANGED"
	}
	if code != "" {
		if err := finishFailure(op, tx, id, code, false); err != nil {
			return Claim{}, err
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, storeError(op)
		}
		return Claim{}, nil
	}
	_, err = tx.ExecContext(op, `
		UPDATE media_curation_jobs
		SET
		    status = 'running',
		    attempts = attempts + 1,
		    claim_token = $2,
		    lease_until = clock_timestamp() + $3 * INTERVAL '1 microsecond',
		    started_at = COALESCE(started_at, clock_timestamp()),
		    failure_code = NULL,
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
	`, id, token, ClaimLease.Microseconds())
	if err != nil {
		return Claim{}, storeError(op)
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, storeError(op)
	}
	return c, nil
}

// Finish atomically publishes the entire candidate batch or a safe failure.
// A committed retry creates a durable outbox event; the future consumer must
// acknowledge that outcome rather than also asking Asynq to retry it.
func (s SQLStore) Finish(ctx context.Context, c Claim, pub *Publication, code string, retry bool) error {
	if s.DB == nil || !c.valid() {
		return ErrConfiguration
	}
	if pub != nil && (!pub.valid || pub.claim != c) {
		return ErrResult
	}
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return storeError(op)
	}
	defer tx.Rollback()
	j, err := lockJob(op, tx, c.AssetID, false)
	if err != nil {
		return err
	}
	if j.status != "running" || !j.live || j.claim.Token != c.Token || j.claim.Attempts != c.Attempts {
		return ErrStale
	}
	if !j.eligible || j.claim.UserID != c.UserID {
		pub, code, retry = nil, "CURATION_SOURCE_CHANGED", false
	} else if j.claim != c {
		return ErrStale
	}
	if pub == nil {
		code = safeCode(code)
		retry = retry && c.Attempts < MaxAttempts && retryableCode(code)
		if err := finishFailure(op, tx, c.AssetID, code, retry); err != nil {
			return err
		}
		if retry {
			dedupe := fmt.Sprintf("curate:%s:%s:retry:%d", c.AssetID, c.TranscriptSHA256, c.Attempts)
			if err := recoveryEvent(op, tx, c.AssetID, dedupe); err != nil {
				return err
			}
		}
	} else {
		for index, candidate := range pub.candidates {
			id, err := newIdentifier()
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(op, `
				INSERT INTO media_curation_candidates (
				    id,
				    asset_id,
				    candidate_index,
				    start_word_index,
				    end_word_index_exclusive,
				    start_ms,
				    end_ms,
				    title,
				    hook,
				    reason
				)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			`, id, c.AssetID, index, candidate.StartWordIndex, candidate.EndWordIndexExclusive,
				candidate.startMs, candidate.endMs, candidate.Title, candidate.Hook, candidate.Reason)
			if err != nil {
				return storeError(op)
			}
		}
		_, err = tx.ExecContext(op, `
			UPDATE media_curation_jobs
			SET
			    status = 'ready',
			    claim_token = NULL,
			    lease_until = NULL,
			    failure_code = NULL,
			    completed_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE asset_id = $1
		`, c.AssetID)
		if err != nil {
			return storeError(op)
		}
	}
	// Recheck the lease after all candidate/outbox writes, not only at entry.
	// The row is still locked, so no competing claim can replace its token.
	var live bool
	if err := tx.QueryRowContext(op, `
		SELECT $1::timestamptz > clock_timestamp()
	`, j.lease.Time).Scan(&live); err != nil {
		return storeError(op)
	}
	if !live {
		return ErrStale
	}
	if err := tx.Commit(); err != nil {
		return storeError(op)
	}
	return nil
}

func finishFailure(ctx context.Context, tx *sql.Tx, id, code string, retry bool) error {
	state := "failed"
	if retry {
		state = "pending"
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE media_curation_jobs
		SET
		    status = $2,
		    claim_token = NULL,
		    lease_until = NULL,
		    failure_code = $3,
		    completed_at = CASE WHEN $2 = 'failed' THEN clock_timestamp() END,
		    next_attempt_at = clock_timestamp() + $4 * INTERVAL '1 microsecond',
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
	`, id, state, code, RetryDelay.Microseconds())
	if err != nil {
		return storeError(ctx)
	}
	return nil
}

func recoveryEvent(ctx context.Context, tx *sql.Tx, id, dedupe string) error {
	eventID, err := newIdentifier()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
		    id,
		    asset_id,
		    task_type,
		    queue_name,
		    payload,
		    dedupe_key,
		    next_attempt_at
		)
		VALUES ($1, $2, $3, $4, jsonb_build_object('assetId', $2::text), $5,
		    clock_timestamp() + $6 * INTERVAL '1 microsecond')
		ON CONFLICT (dedupe_key) DO NOTHING
	`, eventID, id, TaskType, Queue, dedupe, RetryDelay.Microseconds())
	if err != nil {
		return storeError(ctx)
	}
	var same bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM outbox_events
		    WHERE dedupe_key = $1 AND asset_id = $2
		        AND task_type = $3 AND queue_name = $4
		        AND payload = jsonb_build_object('assetId', $2::text)
		)
	`, dedupe, id, TaskType, Queue).Scan(&same); err != nil {
		return storeError(ctx)
	}
	if !same {
		return ErrConflict
	}
	return nil
}

func safeCode(code string) string {
	switch code {
	case "CURATION_SOURCE_CHANGED", "CURATION_INPUT_TOO_LARGE", "CURATION_INVALID_OUTPUT",
		"CURATION_DOWNLOAD_FAILED", "CURATION_PROVIDER_FAILED", "CURATION_PROVIDER_AUTH",
		"CURATION_PROVIDER_REJECTED", "CURATION_TIMEOUT", "CURATION_STORAGE_FAILED",
		"CURATION_INTERRUPTED", "CURATION_RETRIES_EXHAUSTED":
		return code
	default:
		return "CURATION_FAILED"
	}
}

func retryableCode(code string) bool {
	switch code {
	case "CURATION_DOWNLOAD_FAILED", "CURATION_PROVIDER_FAILED", "CURATION_TIMEOUT",
		"CURATION_STORAGE_FAILED", "CURATION_INTERRUPTED":
		return true
	default:
		return false
	}
}

func storeError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrDatabase
}

func newIdentifier() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrDatabase
	}
	return hex.EncodeToString(id[:]), nil
}
