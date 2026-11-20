-- +goose Up
-- Independent preparation state; no historical asset backfill.
CREATE TABLE media_preparations (
    asset_id text PRIMARY KEY REFERENCES media_assets(id),
    pipeline_version integer NOT NULL DEFAULT 1 CHECK (pipeline_version = 1),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'preparing', 'ready', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 2),
    failure_code text CHECK (failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    claim_token text CHECK (claim_token IS NULL OR btrim(claim_token) <> ''),
    lease_until timestamptz,
    output_id text CHECK (output_id IS NULL OR output_id ~ '^[0-9a-f]{32}$'),
    storage_bucket text,
    audio_key text,
    audio_sha256 text,
    source_sha256 text,
    audio_size_bytes bigint,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    prepared_at timestamptz,
    CONSTRAINT media_preparations_claim_complete CHECK (
        (status = 'preparing') = (claim_token IS NOT NULL)
        AND (claim_token IS NULL) = (lease_until IS NULL)
        AND (claim_token IS NULL) = (output_id IS NULL)
        AND (claim_token IS NULL OR attempts > 0)
    ),
    CONSTRAINT media_preparations_artifacts_complete CHECK (
        CASE WHEN status = 'ready' THEN
            prepared_at IS NOT NULL
            AND storage_bucket IS NOT NULL AND btrim(storage_bucket) <> ''
            AND audio_key IS NOT NULL AND btrim(audio_key) <> ''
            AND audio_sha256 IS NOT NULL AND audio_sha256 ~ '^[0-9a-f]{64}$'
            AND source_sha256 IS NOT NULL AND source_sha256 ~ '^[0-9a-f]{64}$'
            AND audio_size_bytes IS NOT NULL AND audio_size_bytes BETWEEN 1 AND 240000000
            AND attempts > 0 AND failure_code IS NULL
        ELSE
            prepared_at IS NULL AND storage_bucket IS NULL
            AND audio_key IS NULL AND audio_sha256 IS NULL AND source_sha256 IS NULL
            AND audio_size_bytes IS NULL
        END
    ),
    CONSTRAINT media_preparations_failure_state CHECK (
        (status = 'failed' AND failure_code IS NOT NULL)
        OR (status = 'pending')
        OR (status IN ('preparing', 'ready') AND failure_code IS NULL)
    )
);

CREATE INDEX media_preparations_expired_idx
    ON media_preparations (lease_until, asset_id)
    WHERE status = 'preparing';

CREATE INDEX outbox_events_preparation_asset_idx
    ON outbox_events (asset_id, created_at DESC, id DESC)
    WHERE task_type = 'media:prepare' AND queue_name = 'preparation';

CREATE INDEX outbox_events_process_asset_idx
    ON outbox_events (asset_id, created_at DESC, id DESC)
    WHERE task_type = 'media:process' AND queue_name = 'processing';

-- +goose Down
-- Disposable schemas only: never roll back live derivative references.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media_preparations WHERE status IN ('preparing', 'ready')) THEN
        RAISE EXCEPTION 'preparation claims and ready outputs must be drained before rollback';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP INDEX outbox_events_preparation_asset_idx;
DROP INDEX outbox_events_process_asset_idx;
DROP TABLE media_preparations;
