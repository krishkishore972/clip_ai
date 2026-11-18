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

// Resolve atomically provisions exactly one application user per identity.
// No profile claims or email addresses are accepted as linkage keys.
func (s Store) Resolve(ctx context.Context, issuer, subject string) (Principal, error) {
	const findUserQuery = `
		SELECT id
		FROM users
		WHERE clerk_issuer = $1
			AND clerk_subject = $2
	`

	var p Principal
	err := s.DB.QueryRowContext(
		ctx,
		findUserQuery,
		issuer,
		subject,
	).Scan(&p.UserID)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Principal{}, fmt.Errorf("find user: %w", err)
	}
	const resolveUserQuery = `
		INSERT INTO users (
			id,
			clerk_issuer,
			clerk_subject
		)
		VALUES ($1, $2, $3)
		ON CONFLICT (clerk_issuer, clerk_subject) DO UPDATE
		SET updated_at = users.updated_at
		RETURNING id
	`

	err = s.DB.QueryRowContext(
		ctx,
		resolveUserQuery,
		newID(),
		issuer,
		subject,
	).Scan(&p.UserID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve user: %w", err)
	}
	return p, nil
}
