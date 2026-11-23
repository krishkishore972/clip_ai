-- +goose Up
-- S3-backed transcripts are independent of historical inline transcripts.
-- No automatic backfill or modification of media_transcriptions.
CREATE TABLE media_transcript_files (
    asset_id text PRIMARY KEY REFERENCES media_assets(id),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'transcribing', 'ready', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 2),
    requested_language text NOT NULL
        CHECK (requested_language IN ('multi', 'te', 'hi', 'en', 'ta', 'kn', 'ml')),
    storage_bucket text NOT NULL CHECK (btrim(storage_bucket) <> ''),
    audio_key text NOT NULL CHECK (btrim(audio_key) <> ''),
    audio_sha256 text NOT NULL CHECK (audio_sha256 ~ '^[0-9a-f]{64}$'),
    audio_size_bytes bigint NOT NULL CHECK (audio_size_bytes BETWEEN 1 AND 240000000),
    audio_start_ms bigint NOT NULL CHECK (audio_start_ms BETWEEN -86400000 AND 86400000),
    source_duration_ms bigint NOT NULL CHECK (source_duration_ms BETWEEN 1 AND 7200000),
    claim_token text,
    output_id text,
    lease_until timestamptz,
    failure_code text CHECK (failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    transcript_key text,
    transcript_sha256 text,
    transcript_size_bytes bigint,
    word_count integer,
    duration_ms bigint,
    model text,
    recovery_generation bigint NOT NULL DEFAULT 0 CHECK (recovery_generation >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at timestamptz,
    CONSTRAINT transcript_files_claim CHECK (
        (status = 'transcribing') = (claim_token IS NOT NULL)
        AND (claim_token IS NULL) = (lease_until IS NULL)
        AND (claim_token IS NULL) = (output_id IS NULL)
        AND (
            claim_token IS NULL
            OR (
                btrim(claim_token) <> ''
                AND btrim(output_id) <> ''
                AND output_id <> claim_token
                AND attempts > 0
            )
        )
    ),
    CONSTRAINT transcript_files_result CHECK (
        CASE WHEN status = 'ready' THEN
            completed_at IS NOT NULL
            AND transcript_key IS NOT NULL AND btrim(transcript_key) <> ''
            AND transcript_sha256 IS NOT NULL AND transcript_sha256 ~ '^[0-9a-f]{64}$'
            AND transcript_size_bytes IS NOT NULL AND transcript_size_bytes BETWEEN 1 AND 16777216
            AND word_count IS NOT NULL AND word_count BETWEEN 0 AND 200000
            AND duration_ms IS NOT NULL AND duration_ms BETWEEN 1 AND 86400000
            AND model IS NOT NULL AND btrim(model) <> ''
            AND attempts > 0 AND failure_code IS NULL
        ELSE
            completed_at IS NULL
            AND transcript_key IS NULL AND transcript_sha256 IS NULL
            AND transcript_size_bytes IS NULL AND word_count IS NULL
            AND duration_ms IS NULL AND model IS NULL
        END
    ),
    CONSTRAINT transcript_files_failure CHECK (
        (status = 'failed' AND failure_code IS NOT NULL)
        OR (status = 'pending')
        OR (status = 'transcribing' AND failure_code IS NULL)
        OR (status = 'ready' AND failure_code IS NULL)
    )
);

CREATE INDEX media_transcript_files_recovery_idx
    ON media_transcript_files (updated_at, asset_id)
    WHERE status IN ('pending', 'transcribing');

-- +goose Down
-- Do not discard durable work or private-object references.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media_transcript_files) THEN
        RAISE EXCEPTION 'refusing rollback of nonempty media_transcript_files';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE media_transcript_files;
