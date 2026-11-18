package upload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/krishkishore972/clip_ai/internal/auth"
)

type CompletedAsset struct {
	ID, Bucket, Key, Filename, ContentType string
	SizeBytes                              int64
}

type AssetRecorder interface {
	SaveCompleted(context.Context, CompletedAsset) error
}

type SQLAssets struct{ DB *sql.DB }

// SaveCompleted does not adopt legacy/unowned rows or overwrite media metadata.
// Retries succeed only when all immutable fields and ownership match.
func (s SQLAssets) SaveCompleted(ctx context.Context, in CompletedAsset) error {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	const query = `
		INSERT INTO media_assets (
			id,
			user_id,
			storage_bucket,
			storage_key,
			original_filename,
			declared_content_type,
			size_bytes
		)
		SELECT
			$1,
			$2,
			$3,
			$4,
			$5,
			$6,
			$7
		WHERE EXISTS (
			SELECT 1
			FROM users
			WHERE id = $2
		)
		ON CONFLICT (id) DO UPDATE
		SET id = EXCLUDED.id
		WHERE media_assets.user_id = EXCLUDED.user_id
			AND media_assets.storage_bucket = EXCLUDED.storage_bucket
			AND media_assets.storage_key = EXCLUDED.storage_key
			AND media_assets.original_filename = EXCLUDED.original_filename
			AND media_assets.declared_content_type = EXCLUDED.declared_content_type
			AND media_assets.size_bytes = EXCLUDED.size_bytes
		RETURNING id
	`

	var id string
	err := s.DB.QueryRowContext(
		ctx,
		query,
		in.ID,
		p.UserID,
		in.Bucket,
		in.Key,
		in.Filename,
		in.ContentType,
		in.SizeBytes,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("persist completed asset: %w", err)
	}
	return nil
}
