package job

import (
	"context"
	"errors"

	"github.com/hibiken/asynq"
	"github.com/krishkishore972/clip_ai/internal/preparation"
)

// preparation.Delivery identifies a committed dispatch, not user-supplied input.

type PreparationArchiveStore interface {
	ArchiveCandidates(context.Context, string, int) ([]preparation.Delivery, error)
	FailArchived(context.Context, preparation.Delivery) error
}

// SetArchiveInspector must be called before starting the worker. Inspection is
// read-only: tasks are not deleted, replayed, or modified by this reconciliation.
func (h *PreparationHandler) SetArchiveInspector(inspector ValidationTaskInspector) {
	h.inspector = inspector
}

// ReconcileArchived records terminal failures that occurred before a claim
// could be saved (e.g. PostgreSQL down for all four queue attempts). It never
// guesses that a missing Redis task was archived; Redis loss is a separate risk.
func (h *PreparationHandler) ReconcileArchived(ctx context.Context) error {
	store, ok := h.store.(PreparationArchiveStore)
	if !ok || h.inspector == nil {
		return nil
	}
	deliveries, err := store.ArchiveCandidates(ctx, h.archiveCursor, 100)
	if err != nil {
		return preparation.ErrDatabase
	}
	if len(deliveries) == 0 {
		h.archiveCursor = ""
		return nil
	}
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, err := h.inspector.GetTaskInfo(preparation.Queue, delivery.EventID)
		if errors.Is(err, asynq.ErrTaskNotFound) || (err == nil && info != nil && info.State != asynq.TaskStateArchived) {
			h.archiveCursor = delivery.AssetID
			continue
		}
		if err != nil || info == nil {
			return errors.New("preparation: queue inspection unavailable")
		}
		if err := store.FailArchived(ctx, delivery); err != nil {
			return preparation.ErrDatabase
		}
		h.archiveCursor = delivery.AssetID
	}
	return nil
}
