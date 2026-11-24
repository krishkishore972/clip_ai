package transcription

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/krishkishore972/clip_ai/internal/curation"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

const databaseTimeout = 5 * time.Second

var (
	ErrDatabase = errors.New("transcription: database operation failed")
	ErrStale    = errors.New("transcription: stale claim")
	ErrBusy     = errors.New("transcription: claim busy")
)

// Claim snapshots trusted immutable preparation metadata, never client input.
type Claim struct {
	AssetID, Bucket, AudioKey, AudioSHA256 string
	AudioSizeBytes, AudioStartMs           int64
	SourceDurationMs                       int64
	Language, Token, OutputID              string
	Attempts                               int
}

func (c Claim) ValidSource() bool {
	prefix := "dev-uploads/" + c.AssetID + "/derived/v1/"
	part := strings.TrimSuffix(strings.TrimPrefix(c.AudioKey, prefix), "/audio.wav")
	return identifier.MatchString(c.AssetID) && c.Bucket != "" &&
		strings.HasPrefix(c.AudioKey, prefix) && strings.HasSuffix(c.AudioKey, "/audio.wav") &&
		identifier.MatchString(part) && checksum.MatchString(c.AudioSHA256) &&
		c.AudioSizeBytes > 0 && c.AudioSizeBytes <= media.MaxPreparedAudioBytes &&
		c.AudioStartMs >= -media.MaxAudioStartMs && c.AudioStartMs <= media.MaxAudioStartMs &&
		c.SourceDurationMs > 0 && c.SourceDurationMs <= media.MaxDurationSeconds*1000 &&
		ValidLanguage(c.Language)
}

type Publication struct {
	Object     storage.DerivedObject
	WordCount  int
	DurationMs int64
}

type SQLStore struct{ DB *sql.DB }

func CheckFileSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDatabase
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
		    asset_id,
		    status,
		    attempts,
		    requested_language,
		    storage_bucket,
		    audio_key,
		    audio_sha256,
		    audio_size_bytes,
		    audio_start_ms,
		    source_duration_ms,
		    claim_token,
		    output_id,
		    lease_until,
		    failure_code,
		    transcript_key,
		    transcript_sha256,
		    transcript_size_bytes,
		    word_count,
		    duration_ms,
		    model,
		    recovery_generation,
		    next_attempt_at,
		    updated_at
		FROM media_transcript_files
		LIMIT 0
	`)
	if err != nil {
		return ErrDatabase
	}
	if err := rows.Close(); err != nil {
		return ErrDatabase
	}
	return nil
}

// InsertFileJob runs inside preparation's publication transaction.
func InsertFileJob(ctx context.Context, tx *sql.Tx, id, language string) error {
	if tx == nil || !identifier.MatchString(id) || !ValidLanguage(language) {
		return ErrConfiguration
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO media_transcript_files (
		    asset_id,
		    requested_language,
		    storage_bucket,
		    audio_key,
		    audio_sha256,
		    audio_size_bytes,
		    audio_start_ms,
		    source_duration_ms
		)
		SELECT
		    a.id,
		    $2,
		    p.storage_bucket,
		    p.audio_key,
		    p.audio_sha256,
		    p.audio_size_bytes,
		    p.audio_start_ms,
		    a.duration_ms
		FROM media_assets a
		JOIN media_preparations p ON p.asset_id = a.id
		WHERE a.id = $1
		    AND a.user_id IS NOT NULL
		    AND a.status = 'ready'
		    AND p.status = 'ready'
		ON CONFLICT (asset_id) DO NOTHING
	`, id, language)
	if err != nil {
		return dbError(ctx)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return dbError(ctx)
	}
	if n == 0 {
		return nil
	}
	return fileEvent(ctx, tx, id, "transcript-file:"+id+":v1", 0)
}

func fileEvent(ctx context.Context, tx *sql.Tx, id, dedupe string, delay time.Duration) error {
	eventID, err := newID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		AssetID string `json:"assetId"`
	}{id})
	if err != nil {
		return ErrDatabase
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
		VALUES (
		    $1, $2, $3, $4, $5, $6,
		    clock_timestamp() + $7 * INTERVAL '1 microsecond'
		)
		ON CONFLICT (dedupe_key) DO NOTHING
	`, eventID, id, TaskType, Queue, payload, dedupe, delay.Microseconds())
	if err != nil {
		return dbError(ctx)
	}
	return nil
}

func (s SQLStore) Claim(ctx context.Context, id, token string, lease time.Duration) (Claim, error) {
	if s.DB == nil || !identifier.MatchString(id) || !identifier.MatchString(token) || lease <= 0 {
		return Claim{}, ErrConfiguration
	}
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return Claim{}, dbError(op)
	}
	defer tx.Rollback()
	var c Claim
	var state string
	var due, eligible bool
	err = tx.QueryRowContext(op, `
		SELECT
		    t.asset_id,
		    t.storage_bucket,
		    t.audio_key,
		    t.audio_sha256,
		    t.audio_size_bytes,
		    t.audio_start_ms,
		    t.source_duration_ms,
		    t.requested_language,
		    t.attempts,
		    t.status,
		    t.next_attempt_at <= clock_timestamp(),
		    COALESCE(
		        a.user_id IS NOT NULL
		        AND a.status = 'ready'
		        AND p.status = 'ready'
		        AND p.storage_bucket = t.storage_bucket
		        AND p.audio_key = t.audio_key
		        AND p.audio_sha256 = t.audio_sha256
		        AND p.audio_size_bytes = t.audio_size_bytes
		        AND p.audio_start_ms = t.audio_start_ms
		        AND a.duration_ms = t.source_duration_ms,
		        FALSE
		    )
		FROM media_transcript_files t
		JOIN media_assets a ON a.id = t.asset_id
		LEFT JOIN media_preparations p ON p.asset_id = t.asset_id
		WHERE t.asset_id = $1
		FOR UPDATE OF t
	`, id).Scan(
		&c.AssetID, &c.Bucket, &c.AudioKey, &c.AudioSHA256,
		&c.AudioSizeBytes, &c.AudioStartMs, &c.SourceDurationMs,
		&c.Language, &c.Attempts, &state, &due, &eligible,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, nil
	}
	if err != nil {
		return Claim{}, dbError(op)
	}
	if state == "transcribing" {
		return Claim{}, ErrBusy
	}
	if state != "pending" || !due {
		return Claim{}, nil
	}
	if !c.ValidSource() || !eligible {
		if err := failFile(op, tx, id, "TRANSCRIPTION_SOURCE_CHANGED", false); err != nil {
			return Claim{}, err
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, dbError(op)
		}
		return Claim{}, nil
	}
	if c.Attempts >= MaxAttempts {
		if err := failFile(op, tx, id, "TRANSCRIPTION_RETRIES_EXHAUSTED", false); err != nil {
			return Claim{}, err
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, dbError(op)
		}
		return Claim{}, nil
	}
	outputID, err := newID()
	if err != nil {
		return Claim{}, err
	}
	for outputID == token {
		outputID, err = newID()
		if err != nil {
			return Claim{}, err
		}
	}
	_, err = tx.ExecContext(op, `
		UPDATE media_transcript_files
		SET
		    status = 'transcribing',
		    attempts = attempts + 1,
		    claim_token = $2,
		    output_id = $3,
		    lease_until = clock_timestamp() + $4 * INTERVAL '1 microsecond',
		    failure_code = NULL,
		    updated_at = clock_timestamp()
		WHERE asset_id = $1
	`, id, token, outputID, lease.Microseconds())
	if err != nil {
		return Claim{}, dbError(op)
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, dbError(op)
	}
	c.Token, c.OutputID, c.Attempts = token, outputID, c.Attempts+1
	return c, nil
}

func TranscriptKey(assetID, outputID string) string {
	return "dev-uploads/" + assetID + "/derived/v1/" + outputID + "/transcript.json"
}

func (s SQLStore) Finish(ctx context.Context, c Claim, pub *Publication, code string, retry bool) error {
	if s.DB == nil || !c.ValidSource() || !identifier.MatchString(c.Token) ||
		!identifier.MatchString(c.OutputID) || c.Token == c.OutputID {
		return ErrConfiguration
	}
	if pub != nil && (pub.Object.Key != TranscriptKey(c.AssetID, c.OutputID) ||
		!checksum.MatchString(pub.Object.SHA256) || pub.Object.SizeBytes <= 0 ||
		pub.Object.SizeBytes > MaxDocumentBytes || pub.WordCount < 0 || pub.WordCount > MaxWords ||
		pub.DurationMs <= 0 || pub.DurationMs > 86400000) {
		return ErrResponse
	}
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return dbError(op)
	}
	defer tx.Rollback()
	var attempts int
	var live bool
	err = tx.QueryRowContext(op, `
		SELECT
		    attempts,
		    COALESCE(
		        status = 'transcribing'
		        AND claim_token = $2
		        AND output_id = $3
		        AND lease_until > clock_timestamp(),
		        FALSE
		    )
		FROM media_transcript_files
		WHERE asset_id = $1
		FOR UPDATE
	`, c.AssetID, c.Token, c.OutputID).Scan(&attempts, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return dbError(op)
	}
	if !live || attempts != c.Attempts {
		return ErrStale
	}
	if pub == nil {
		retry = retry && attempts < MaxAttempts
		if err := failFile(op, tx, c.AssetID, safeFileCode(code), retry); err != nil {
			return err
		}
		if retry {
			dedupe := fmt.Sprintf("transcript-file:%s:retry:%d", c.AssetID, attempts)
			if err := fileEvent(op, tx, c.AssetID, dedupe, time.Minute); err != nil {
				return err
			}
		}
	} else {
		result, err := tx.ExecContext(op, `
			UPDATE media_transcript_files
			SET
			    status = 'ready',
			    claim_token = NULL,
			    output_id = NULL,
			    lease_until = NULL,
			    failure_code = NULL,
			    transcript_key = $4,
			    transcript_sha256 = $5,
			    transcript_size_bytes = $6,
			    word_count = $7,
			    duration_ms = $8,
			    model = 'nova-3',
			    completed_at = CURRENT_TIMESTAMP,
			    updated_at = CURRENT_TIMESTAMP
			WHERE asset_id = $1
			    AND claim_token = $2
			    AND output_id = $3
			    AND lease_until > clock_timestamp()
		`, c.AssetID, c.Token, c.OutputID, pub.Object.Key, pub.Object.SHA256,
			pub.Object.SizeBytes, pub.WordCount, pub.DurationMs)
		if err != nil {
			return dbError(op)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return dbError(op)
		}
		if n != 1 {
			return ErrStale
		}
		// Publication and the initial curation handoff must commit together.
		// Finish already owns the transcript lock; the helper then locks curation.
		if _, err := curation.EnqueueReadyTranscript(op, tx, c.AssetID); err != nil {
			return fmt.Errorf("transcription: curation handoff: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return dbError(op)
	}
	return nil
}

func failFile(ctx context.Context, tx *sql.Tx, id, code string, retry bool) error {
	state := "failed"
	if retry {
		state = "pending"
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE media_transcript_files
		SET
		    status = $2,
		    claim_token = NULL,
		    output_id = NULL,
		    lease_until = NULL,
		    failure_code = $3,
		    updated_at = clock_timestamp(),
		    next_attempt_at = clock_timestamp() + INTERVAL '1 minute'
		WHERE asset_id = $1
	`, id, state, code)
	if err != nil {
		return dbError(ctx)
	}
	return nil
}

// Recover repairs both abandoned claims and pending deliveries lost before a
// claim. Repair generations produce new queue IDs; claims prevent duplicate work.
func (s SQLStore) Recover(ctx context.Context, limit int) (int, error) {
	if s.DB == nil || limit < 1 || limit > 100 {
		return 0, ErrConfiguration
	}
	op, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	tx, err := s.DB.BeginTx(op, nil)
	if err != nil {
		return 0, dbError(op)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(op, `
		SELECT asset_id, attempts, recovery_generation
		FROM media_transcript_files
		WHERE (status = 'transcribing' AND lease_until <= clock_timestamp())
		    OR (status = 'pending' AND updated_at < clock_timestamp() - INTERVAL '30 minutes')
		ORDER BY updated_at, asset_id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return 0, dbError(op)
	}
	type item struct {
		id       string
		attempts int
		gen      int64
	}
	var items []item
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.id, &i.attempts, &i.gen); err != nil {
			rows.Close()
			return 0, dbError(op)
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, dbError(op)
	}
	for _, i := range items {
		retry := i.attempts < MaxAttempts
		code := "TRANSCRIPTION_INTERRUPTED"
		if !retry {
			code = "TRANSCRIPTION_RETRIES_EXHAUSTED"
		}
		if err := failFile(op, tx, i.id, code, retry); err != nil {
			return 0, err
		}
		if !retry {
			continue
		}
		if _, err := tx.ExecContext(op, `
			UPDATE media_transcript_files
			SET recovery_generation = recovery_generation + 1
			WHERE asset_id = $1
		`, i.id); err != nil {
			return 0, dbError(op)
		}
		dedupe := fmt.Sprintf("transcript-file:%s:recover:%d", i.id, i.gen+1)
		if err := fileEvent(op, tx, i.id, dedupe, time.Minute); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, dbError(op)
	}
	return len(items), nil
}

func safeFileCode(code string) string {
	switch code {
	case "TRANSCRIPTION_DOWNLOAD_FAILED", "TRANSCRIPTION_SOURCE_CHANGED",
		"TRANSCRIPTION_PROVIDER_AUTH", "TRANSCRIPTION_PROVIDER_REJECTED",
		"TRANSCRIPTION_PROVIDER_FAILED", "TRANSCRIPTION_TIMEOUT",
		"TRANSCRIPTION_INVALID_OUTPUT", "TRANSCRIPTION_UPLOAD_FAILED",
		"TRANSCRIPTION_STORAGE_FAILED", "TRANSCRIPTION_INTERRUPTED",
		"TRANSCRIPTION_RETRIES_EXHAUSTED":
		return code
	default:
		return "TRANSCRIPTION_FAILED"
	}
}

func dbError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrDatabase
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrDatabase
	}
	return hex.EncodeToString(id[:]), nil
}
