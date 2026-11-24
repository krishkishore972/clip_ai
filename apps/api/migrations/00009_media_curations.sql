-- +goose Up
-- One initial curation job per asset. Preferences are supplied explicitly by the
-- client and persisted by the future owned upload-completion flow.
-- No backfill, queue event, provider request or change to existing tables.
CREATE TABLE media_curation_jobs (
    asset_id text PRIMARY KEY REFERENCES media_assets(id),
    candidate_count integer NOT NULL CHECK (candidate_count BETWEEN 1 AND 5),
    min_duration_ms bigint NOT NULL CHECK (min_duration_ms BETWEEN 1 AND 60000),
    max_duration_ms bigint NOT NULL CHECK (
        max_duration_ms BETWEEN min_duration_ms AND 60000
    ),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'ready', 'failed')),

    -- Counts durable worker claims, not the SDK's HTTP attempts. Retry policy
    -- remains a worker concern; this migration does not authorize extra calls.
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    claim_token text CHECK (claim_token IS NULL OR btrim(claim_token) <> ''),
    lease_until timestamptz,
    failure_code text CHECK (
        failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_]{0,63}$'
    ),
    recovery_generation bigint NOT NULL DEFAULT 0 CHECK (recovery_generation >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Snapshot the exact private transcript and source used for this run.
    -- All are NULL while waiting for transcription, then populated together.
    transcript_bucket text,
    transcript_key text,
    transcript_sha256 text,
    transcript_size_bytes bigint,
    transcript_word_count integer,
    source_duration_ms bigint,
    model text NOT NULL DEFAULT 'openai/gpt-oss-120b'
        CHECK (btrim(model) <> '' AND char_length(model) <= 128),
    prompt_version text NOT NULL DEFAULT 'v1'
        CHECK (prompt_version ~ '^v[1-9][0-9]{0,8}$'),

    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at timestamptz,
    completed_at timestamptz,

    CONSTRAINT media_curation_jobs_snapshot CHECK (
        (
            transcript_bucket IS NULL
            AND transcript_key IS NULL
            AND transcript_sha256 IS NULL
            AND transcript_size_bytes IS NULL
            AND transcript_word_count IS NULL
            AND source_duration_ms IS NULL
        )
        OR (
            transcript_bucket IS NOT NULL AND btrim(transcript_bucket) <> ''
            AND transcript_key IS NOT NULL AND btrim(transcript_key) <> ''
            AND transcript_sha256 IS NOT NULL
                AND transcript_sha256 ~ '^[0-9a-f]{64}$'
            AND transcript_size_bytes IS NOT NULL
                AND transcript_size_bytes BETWEEN 1 AND 16777216
            AND transcript_word_count IS NOT NULL
                AND transcript_word_count BETWEEN 0 AND 12000
            AND source_duration_ms IS NOT NULL
                AND source_duration_ms BETWEEN 1 AND 7200000
        )
    ),
    CONSTRAINT media_curation_jobs_claim CHECK (
        (status = 'running') = (claim_token IS NOT NULL)
        AND (claim_token IS NULL) = (lease_until IS NULL)
        AND (
            status <> 'running'
            OR (
                attempts > 0
                AND started_at IS NOT NULL
                AND transcript_sha256 IS NOT NULL
            )
        )
    ),
    CONSTRAINT media_curation_jobs_completion CHECK (
        (status IN ('ready', 'failed')) = (completed_at IS NOT NULL)
        AND (
            status <> 'ready'
            OR (
                attempts > 0
                AND started_at IS NOT NULL
                AND transcript_sha256 IS NOT NULL
            )
        )
    ),
    CONSTRAINT media_curation_jobs_failure CHECK (
        (status = 'failed' AND failure_code IS NOT NULL)
        OR (status = 'pending')
        OR (status IN ('running', 'ready') AND failure_code IS NULL)
    )
);

CREATE INDEX media_curation_jobs_pending_idx
    ON media_curation_jobs (next_attempt_at, asset_id)
    WHERE status = 'pending' AND transcript_sha256 IS NOT NULL;

CREATE INDEX media_curation_jobs_expired_idx
    ON media_curation_jobs (lease_until, asset_id)
    WHERE status = 'running';

-- These are proposals only, not accepted projects or playable previews.
-- The future Go publisher must validate each row against its parent snapshot
-- and publish the full candidate batch and ready state in one transaction.
CREATE TABLE media_curation_candidates (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$'),
    asset_id text NOT NULL REFERENCES media_curation_jobs(asset_id),
    candidate_index integer NOT NULL CHECK (candidate_index BETWEEN 0 AND 4),
    start_word_index integer NOT NULL CHECK (
        start_word_index BETWEEN 0 AND 11999
    ),
    end_word_index_exclusive integer NOT NULL CHECK (
        end_word_index_exclusive > start_word_index
        AND end_word_index_exclusive <= 12000
    ),
    start_ms bigint NOT NULL CHECK (start_ms BETWEEN 0 AND 7199999),
    end_ms bigint NOT NULL CHECK (
        end_ms > start_ms
        AND end_ms <= 7200000
        AND end_ms - start_ms <= 60000
    ),
    title text NOT NULL CHECK (btrim(title) <> '' AND char_length(title) <= 1000),
    hook text NOT NULL CHECK (btrim(hook) <> '' AND char_length(hook) <= 1000),
    reason text NOT NULL CHECK (btrim(reason) <> '' AND char_length(reason) <= 1000),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT media_curation_candidates_order UNIQUE (asset_id, candidate_index),
    CONSTRAINT media_curation_candidates_range UNIQUE (
        asset_id,
        start_word_index,
        end_word_index_exclusive
    )
);

-- +goose Down
-- Lock before checking so a concurrent insert cannot race the empty-only guard.
LOCK TABLE media_curation_jobs, media_curation_candidates IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media_curation_jobs)
        OR EXISTS (SELECT 1 FROM media_curation_candidates) THEN
        RAISE EXCEPTION 'refusing rollback of nonempty media curation tables';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE media_curation_candidates;
DROP TABLE media_curation_jobs;
