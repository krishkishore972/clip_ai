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
	var id string
	err := s.DB.QueryRowContext(ctx, `INSERT INTO media_assets
		(id, workspace_id, storage_bucket, storage_key, original_filename, declared_content_type, size_bytes)
		SELECT $1,$2,$3,$4,$5,$6,$7 WHERE EXISTS (
			SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id
			WHERE m.workspace_id=$2 AND m.user_id=$8 AND m.role='owner' AND w.personal_user_id=$8
		) ON CONFLICT (id) DO UPDATE SET id=EXCLUDED.id
		WHERE media_assets.workspace_id=EXCLUDED.workspace_id
		AND media_assets.storage_bucket=EXCLUDED.storage_bucket AND media_assets.storage_key=EXCLUDED.storage_key
		AND media_assets.original_filename=EXCLUDED.original_filename
		AND media_assets.declared_content_type=EXCLUDED.declared_content_type AND media_assets.size_bytes=EXCLUDED.size_bytes
		RETURNING id`, in.ID, p.WorkspaceID, in.Bucket, in.Key, in.Filename, in.ContentType, in.SizeBytes, p.UserID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("persist completed asset: %w", err)
	}
	return nil
}
