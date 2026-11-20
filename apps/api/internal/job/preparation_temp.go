package job

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (h *PreparationHandler) newAttemptDirectory() (string, error) {
	// Expiry is encoded in a directory we exclusively create. A fresh process
	// can remove crashed attempts only after their entire possible work budget.
	expires := time.Now().Add(h.options.TaskTimeout + 2*time.Minute).Unix()
	return os.MkdirTemp(h.options.TempDir, "attempt-"+strconv.FormatInt(expires, 10)+"-")
}

func (h *PreparationHandler) ownedAttemptPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Dir(path) == h.options.TempDir &&
		attemptDirectoryPattern.MatchString(filepath.Base(path)) &&
		filepath.Clean(path) == path
}

// RemoveAttemptDirectory removes only a direct, named attempt under the private root.
// Paths outside that root are refused without touching their contents.
func (h *PreparationHandler) RemoveAttemptDirectory(path string) {
	if !h.ownedAttemptPath(path) {
		h.logger.Error("preparation temporary cleanup refused", "code", "TEMP_PATH_INVALID")
		return
	}
	// The absolute path is a direct, named attempt under the private root.
	// RemoveAll does not follow symlinks encountered inside this directory.
	if err := os.RemoveAll(path); err != nil {
		h.logger.Error("preparation temporary cleanup failed", "code", "TEMP_CLEANUP_FAILED")
	}
}

// ScavengeAttempts removes only expired owned attempts, leaving active and unrelated paths.
func (h *PreparationHandler) ScavengeAttempts(ctx context.Context, now time.Time) error {
	root, err := os.Open(h.options.TempDir)
	if err != nil {
		return errors.New("preparation: temporary cleanup unavailable")
	}
	defer root.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := root.ReadDir(100)
		for _, entry := range entries {
			match := attemptDirectoryPattern.FindStringSubmatch(entry.Name())
			if len(match) != 2 || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			expires, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil || expires > now.Unix() {
				continue
			}
			path := filepath.Join(h.options.TempDir, entry.Name())
			if !h.ownedAttemptPath(path) || strings.Contains(entry.Name(), string(filepath.Separator)) {
				continue
			}
			h.RemoveAttemptDirectory(path)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("preparation: temporary cleanup unavailable")
		}
	}
}

// RunRecovery is independent of active task contexts. Recovery creates outbox
// events atomically, so the separately running dispatcher must also be enabled
// for those events to reach Redis. No unfinished attempt is deleted early.
func (h *PreparationHandler) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		// Redis inspection must not consume the budgets for independent DB
		// recovery and local cleanup. Do those first, with separate contexts.
		operationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		count, err := h.store.Recover(operationCtx, 100)
		cancel()
		if err != nil && ctx.Err() == nil {
			h.logger.Error("preparation recovery failed", "code", "PREPARATION_DATABASE")
		} else if count > 0 {
			h.logger.Info("interrupted preparations recovered", "count", count)
		}
		cleanupCtx, stopCleanup := context.WithTimeout(ctx, 10*time.Second)
		if err := h.ScavengeAttempts(cleanupCtx, time.Now()); err != nil && ctx.Err() == nil {
			h.logger.Warn("preparation temporary cleanup deferred", "code", "TEMP_CLEANUP_FAILED")
		}
		stopCleanup()
		archiveCtx, stopArchive := context.WithTimeout(ctx, 10*time.Second)
		if err := h.ReconcileArchived(archiveCtx); err != nil && ctx.Err() == nil {
			h.logger.Error("preparation archive reconciliation failed", "code", "PREPARATION_RECONCILE_FAILED")
		}
		stopArchive()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
