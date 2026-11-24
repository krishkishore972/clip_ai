-- +goose Up
-- Destructive: the user confirmed legacy inline transcript data is disposable.
-- Preserve current S3 transcript files, curation tables and outbox event rows.
-- IF EXISTS also supports a development database where the table/index was
-- already removed manually. Do not use CASCADE: unexpected dependencies must fail.
DROP TABLE IF EXISTS media_transcriptions;
DROP INDEX IF EXISTS outbox_events_transcribe_asset_idx;

-- +goose Down
-- Recreating an empty table cannot restore the deleted transcript data.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'migration 00010 is irreversible: deleted legacy transcripts cannot be restored';
END;
$$;
-- +goose StatementEnd
