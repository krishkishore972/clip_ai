-- +goose Up
-- Independent transcription state; no historical asset backfill.
CREATE TABLE media_transcriptions (
    asset_id text PRIMARY KEY REFERENCES media_assets(id),
    pipeline_version integer NOT NULL DEFAULT 1 CHECK (pipeline_version = 1),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'transcribing', 'ready', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 2),
    failure_code text CHECK (failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    claim_token text CHECK (claim_token IS NULL OR btrim(claim_token) <> ''),
    lease_until timestamptz,
    provider text,
    model text,
    language text,
    request_id text,
    transcript text,
    words jsonb,
    word_count integer,
    duration_ms bigint,
    audio_start_ms bigint,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at timestamptz,
    CONSTRAINT media_transcriptions_claim_complete CHECK (
        (status = 'transcribing') = (claim_token IS NOT NULL)
        AND (claim_token IS NULL) = (lease_until IS NULL)
        AND (claim_token IS NULL OR attempts > 0)
    ),
    CONSTRAINT media_transcriptions_ready_complete CHECK (
        CASE WHEN status = 'ready' THEN
            completed_at IS NOT NULL
            AND provider IS NOT NULL AND btrim(provider) <> ''
            AND model IS NOT NULL AND btrim(model) <> ''
            AND language IS NOT NULL AND btrim(language) <> ''
            AND transcript IS NOT NULL
            AND words IS NOT NULL AND jsonb_typeof(words) = 'array'
            AND word_count IS NOT NULL AND word_count BETWEEN 0 AND 200000
            AND duration_ms IS NOT NULL AND duration_ms BETWEEN 1 AND 86400000
            AND audio_start_ms IS NOT NULL
                AND audio_start_ms BETWEEN -86400000 AND 86400000
            AND attempts > 0 AND failure_code IS NULL
        ELSE
            completed_at IS NULL
            AND provider IS NULL AND model IS NULL AND language IS NULL
            AND request_id IS NULL
            AND transcript IS NULL AND words IS NULL AND word_count IS NULL
            AND duration_ms IS NULL AND audio_start_ms IS NULL
        END
    ),
    CONSTRAINT media_transcriptions_failure_state CHECK (
        (status = 'failed' AND failure_code IS NOT NULL)
        OR (status = 'pending')
        OR (status IN ('transcribing', 'ready') AND failure_code IS NULL)
    )
);

CREATE INDEX media_transcriptions_expired_idx
    ON media_transcriptions (lease_until, asset_id)
    WHERE status = 'transcribing';

CREATE INDEX outbox_events_transcribe_asset_idx
    ON outbox_events (asset_id, created_at DESC, id DESC)
    WHERE task_type = 'media:transcribe' AND queue_name = 'processing';

-- +goose Down
-- Disposable schemas only: never roll back live transcript references.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media_transcriptions WHERE status IN ('transcribing', 'ready')) THEN
        RAISE EXCEPTION 'transcription claims and ready transcripts must be drained before rollback';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP INDEX outbox_events_transcribe_asset_idx;
DROP INDEX media_transcriptions_expired_idx;
DROP TABLE media_transcriptions;
