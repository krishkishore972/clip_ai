-- +goose Up
CREATE TABLE users (
    id text PRIMARY KEY CHECK (btrim(id) <> ''),
    clerk_issuer text NOT NULL CHECK (btrim(clerk_issuer) <> ''),
    clerk_subject text NOT NULL CHECK (btrim(clerk_subject) <> ''),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (clerk_issuer, clerk_subject)
);

CREATE TABLE workspaces (
    id text PRIMARY KEY CHECK (btrim(id) <> ''),
    personal_user_id text NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE workspace_members (
    workspace_id text NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    user_id text NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
    role text NOT NULL CHECK (role = 'owner'),
    PRIMARY KEY (workspace_id, user_id)
);

-- Keep existing rows unowned and inaccessible. Never guess a backfill owner.
ALTER TABLE media_assets ADD COLUMN workspace_id text REFERENCES workspaces(id) ON DELETE RESTRICT;
CREATE INDEX media_assets_workspace_id_idx ON media_assets (workspace_id) WHERE workspace_id IS NOT NULL;

-- +goose Down
-- Destructive rollback: disposable test databases only.
ALTER TABLE media_assets DROP COLUMN workspace_id;
DROP TABLE workspace_members;
DROP TABLE workspaces;
DROP TABLE users;
