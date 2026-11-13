-- +goose Up
-- Completed original uploads only. Ownership and derived assets are deferred.
CREATE TABLE media_assets (
    id text PRIMARY KEY CHECK (btrim(id) <> ''),
    storage_bucket text NOT NULL CHECK (btrim(storage_bucket) <> ''),
    storage_key text NOT NULL CHECK (btrim(storage_key) <> ''),
    original_filename text,
    declared_content_type text NOT NULL CHECK (btrim(declared_content_type) <> ''),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    status text NOT NULL DEFAULT 'uploaded_unvalidated'
        CHECK (status IN ('uploaded_unvalidated', 'validating', 'ready', 'rejected', 'validation_failed')),
    duration_ms bigint CHECK (duration_ms > 0),
    width integer CHECK (width > 0),
    height integer CHECK (height > 0),
    fps_num integer,
    fps_den integer,
    video_codec text CHECK (btrim(video_codec) <> ''),
    audio_codec text CHECK (btrim(audio_codec) <> ''),
    failure_code text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    validated_at timestamptz,
    CONSTRAINT media_assets_storage_unique UNIQUE (storage_bucket, storage_key),
    CONSTRAINT media_assets_fps_check CHECK (
        (fps_num IS NULL AND fps_den IS NULL) OR
        (fps_num IS NOT NULL AND fps_den IS NOT NULL AND fps_num > 0 AND fps_den > 0)
    ),
    CONSTRAINT media_assets_ready_metadata_check CHECK (
        status <> 'ready' OR (
            validated_at IS NOT NULL AND duration_ms IS NOT NULL AND
            width IS NOT NULL AND height IS NOT NULL AND
            fps_num IS NOT NULL AND fps_den IS NOT NULL AND video_codec IS NOT NULL
        )
    )
);

-- +goose Down
-- Destructive: only exercise this rollback against a disposable test schema.
DROP TABLE media_assets;
