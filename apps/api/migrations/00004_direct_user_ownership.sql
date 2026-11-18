-- +goose Up
-- The default Goose transaction makes the backfill and schema removal atomic.
-- Application code must be deployed together with this migration.
LOCK TABLE media_assets, workspaces, workspace_members IN ACCESS EXCLUSIVE MODE;

-- Refuse to guess ownership if the old personal-owner mapping is inconsistent.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM media_assets a
        WHERE a.workspace_id IS NOT NULL
            AND NOT EXISTS (
                SELECT 1
                FROM workspaces w
                JOIN workspace_members m
                    ON m.workspace_id = w.id
                    AND m.user_id = w.personal_user_id
                    AND m.role = 'owner'
                WHERE w.id = a.workspace_id
            )
    ) THEN
        RAISE EXCEPTION 'Cannot migrate inconsistent asset ownership';
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE media_assets
    ADD COLUMN user_id text REFERENCES users(id) ON DELETE RESTRICT;

-- NULL-owned legacy assets remain NULL and inaccessible.
UPDATE media_assets a
SET user_id = w.personal_user_id
FROM workspaces w
WHERE a.workspace_id = w.id;

CREATE INDEX media_assets_user_id_idx
    ON media_assets (user_id)
    WHERE user_id IS NOT NULL;

ALTER TABLE media_assets DROP COLUMN workspace_id;
DROP TABLE workspace_members;
DROP TABLE workspaces;

-- +goose Down
-- Former workspace identities/membership records cannot be reconstructed.
-- Fail safely rather than inventing ownership or dropping the new owner column.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'Direct user ownership is irreversible; restore a verified backup for rollback';
END;
$$;
-- +goose StatementEnd
