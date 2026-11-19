package job

import (
	"context"
	"errors"

	"github.com/hibiken/asynq"
)

// ValidationDelivery identifies a committed dispatch, not user-supplied input.
type ValidationDelivery struct {
	AssetID, EventID string
}

type ValidationArchiveStore interface {
	ArchiveCandidates(context.Context, string, int) ([]ValidationDelivery, error)
	FailArchived(context.Context, ValidationDelivery) error
}

type ValidationTaskInspector interface {
	GetTaskInfo(string, string) (*asynq.TaskInfo, error)
}

func (s SQLValidationStore) ArchiveCandidates(ctx context.Context, after string, limit int) ([]ValidationDelivery, error) {
	if s.DB == nil || limit <= 0 {
		return nil, ErrValidationDatabase
	}
	const query = `
		SELECT
			a.id,
			e.id
		FROM media_assets a
		JOIN LATERAL (
			SELECT id
			FROM outbox_events
			WHERE asset_id = a.id
				AND task_type = 'media:validate'
				AND queue_name = 'validation'
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) e ON true
		WHERE a.status = 'uploaded_unvalidated'
			AND a.user_id IS NOT NULL
			AND a.id > $1
			AND EXISTS (
				SELECT 1
				FROM outbox_events
				WHERE id = e.id
					AND dispatched_at IS NOT NULL
			)
		ORDER BY a.id
		LIMIT $2
	`
	rows, err := s.DB.QueryContext(ctx, query, after, min(limit, 100))
	if err != nil {
		return nil, ErrValidationDatabase
	}
	defer rows.Close()
	var deliveries []ValidationDelivery
	for rows.Next() {
		var delivery ValidationDelivery
		if err := rows.Scan(&delivery.AssetID, &delivery.EventID); err != nil {
			return nil, ErrValidationDatabase
		}
		deliveries = append(deliveries, delivery)
	}
	if rows.Err() != nil {
		return nil, ErrValidationDatabase
	}
	return deliveries, nil
}

func (s SQLValidationStore) FailArchived(ctx context.Context, delivery ValidationDelivery) error {
	if s.DB == nil || delivery.AssetID == "" || delivery.EventID == "" {
		return ErrValidationDatabase
	}
	// Lock the asset first so a concurrent claim cannot be overwritten. Recheck
	// all conditions after acquiring the lock, including newly created recovery
	// events (whose native retry counters may legitimately start from zero).
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ErrValidationDatabase
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM media_assets WHERE id = $1 FOR UPDATE", delivery.AssetID).Scan(&id); err != nil {
		return ErrValidationDatabase
	}
	const query = `
		UPDATE media_assets a
		SET
			status = 'validation_failed',
			failure_code = 'VALIDATION_RETRIES_EXHAUSTED',
			updated_at = clock_timestamp()
		WHERE a.id = $1
			AND a.status = 'uploaded_unvalidated'
			AND a.user_id IS NOT NULL
			AND a.validation_claim_token IS NULL
			AND $2 = (
				SELECT id
				FROM outbox_events
				WHERE asset_id = a.id
					AND task_type = 'media:validate'
					AND queue_name = 'validation'
				ORDER BY created_at DESC, id DESC
				LIMIT 1
			)
	`
	if _, err := tx.ExecContext(ctx, query, delivery.AssetID, delivery.EventID); err != nil {
		return ErrValidationDatabase
	}
	if err := tx.Commit(); err != nil {
		return ErrValidationDatabase
	}
	return nil
}

// SetArchiveInspector must be called before starting the worker. Inspection is
// read-only: tasks are not deleted, replayed, or modified by this reconciliation.
func (h *ValidationHandler) SetArchiveInspector(inspector ValidationTaskInspector) {
	h.inspector = inspector
}

// ReconcileArchived records terminal failures that occurred before a claim
// could be saved (e.g. PostgreSQL down for all four queue attempts). It never
// guesses that a missing Redis task was archived; Redis loss is a separate risk.
func (h *ValidationHandler) ReconcileArchived(ctx context.Context) error {
	store, ok := h.store.(ValidationArchiveStore)
	if !ok || h.inspector == nil {
		return nil
	}
	deliveries, err := store.ArchiveCandidates(ctx, h.archiveCursor, 100)
	if err != nil {
		return ErrValidationDatabase
	}
	if len(deliveries) == 0 {
		h.archiveCursor = ""
		return nil
	}
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, err := h.inspector.GetTaskInfo(ValidationQueue, delivery.EventID)
		if errors.Is(err, asynq.ErrTaskNotFound) || (err == nil && info != nil && info.State != asynq.TaskStateArchived) {
			h.archiveCursor = delivery.AssetID
			continue
		}
		if err != nil || info == nil {
			return errors.New("validation: queue inspection unavailable")
		}
		if err := store.FailArchived(ctx, delivery); err != nil {
			return ErrValidationDatabase
		}
		h.archiveCursor = delivery.AssetID
	}
	return nil
}
