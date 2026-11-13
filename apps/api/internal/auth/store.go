package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

type Store struct{ DB *sql.DB }

func newID() string {
	id := make([]byte, 16)
	_, _ = rand.Read(id) // crypto/rand.Read never returns an error in supported Go versions.
	return hex.EncodeToString(id)
}

// Resolve atomically provisions exactly one personal workspace per identity.
// No profile claims or email addresses are accepted as linkage keys.
func (s Store) Resolve(ctx context.Context, issuer, subject string) (Principal, error) {
	var p Principal
	err := s.DB.QueryRowContext(ctx, `SELECT u.id,w.id FROM users u
		JOIN workspaces w ON w.personal_user_id=u.id
		JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=u.id AND m.role='owner'
		WHERE u.clerk_issuer=$1 AND u.clerk_subject=$2`, issuer, subject).Scan(&p.UserID, &p.WorkspaceID)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Principal{}, fmt.Errorf("find user: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, fmt.Errorf("begin user transaction: %w", err)
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO users (id, clerk_issuer, clerk_subject)
		VALUES ($1,$2,$3) ON CONFLICT (clerk_issuer, clerk_subject)
		DO UPDATE SET updated_at = users.updated_at RETURNING id`, newID(), issuer, subject).Scan(&p.UserID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve user: %w", err)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO workspaces (id, personal_user_id)
		VALUES ($1,$2) ON CONFLICT (personal_user_id)
		DO UPDATE SET personal_user_id = EXCLUDED.personal_user_id RETURNING id`, newID(), p.UserID).Scan(&p.WorkspaceID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve workspace: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role)
		VALUES ($1,$2,'owner') ON CONFLICT (workspace_id,user_id) DO NOTHING`, p.WorkspaceID, p.UserID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, fmt.Errorf("commit user: %w", err)
	}
	return p, nil
}
