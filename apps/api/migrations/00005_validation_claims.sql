-- +goose Up
ALTER TABLE media_assets
    ADD COLUMN validation_attempts integer NOT NULL DEFAULT 0
        CHECK (validation_attempts BETWEEN 0 AND 4),
    ADD COLUMN validation_claim_token text
        CHECK (validation_claim_token IS NULL OR btrim(validation_claim_token) <> ''),
    ADD COLUMN validation_lease_until timestamptz,
    ADD COLUMN probed_metadata jsonb
        CHECK (probed_metadata IS NULL OR jsonb_typeof(probed_metadata) = 'object'),
    ADD CONSTRAINT media_assets_validation_claim_pair CHECK (
        (validation_claim_token IS NULL) = (validation_lease_until IS NULL)
    ),
    ADD CONSTRAINT media_assets_validation_claim_status CHECK (
        validation_claim_token IS NULL OR status = 'validating'
    );

CREATE INDEX media_assets_validation_expiry_idx
    ON media_assets (validation_lease_until, id)
    WHERE status = 'validating' AND user_id IS NOT NULL;

-- +goose Down
-- Refuse rollback while a worker may still be using the fencing columns.
-- Only exercise rollback in a disposable schema; metadata columns are removed.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM media_assets
        WHERE validation_claim_token IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'validation claims must be drained before rollback';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP INDEX media_assets_validation_expiry_idx;
ALTER TABLE media_assets
    DROP CONSTRAINT media_assets_validation_claim_status,
    DROP CONSTRAINT media_assets_validation_claim_pair,
    DROP COLUMN probed_metadata,
    DROP COLUMN validation_lease_until,
    DROP COLUMN validation_claim_token,
    DROP COLUMN validation_attempts;
