// Package auth verifies external identity and resolves application ownership.
package auth

import "context"

type Principal struct {
	UserID      string `json:"userId"`
	WorkspaceID string `json:"workspaceId"`
}

type principalKey struct{}

// WithPrincipal is for trusted server code after authentication, never client input.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok && p.UserID != "" && p.WorkspaceID != ""
}
