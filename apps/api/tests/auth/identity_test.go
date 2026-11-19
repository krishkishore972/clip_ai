package auth_test

import (
	"context"
	"github.com/krishkishore972/clip_ai/internal/auth"
	"testing"
)

func TestPrincipalRequiresOnlyInternalUser(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"missing", context.Background(), false},
		{"empty user", auth.WithPrincipal(context.Background(), auth.Principal{}), false},
		{"user", auth.WithPrincipal(context.Background(), auth.Principal{UserID: "internal-user"}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := auth.FromContext(tc.ctx)
			if ok != tc.want {
				t.Fatalf("authenticated principal = %t, want %t", ok, tc.want)
			}
		})
	}
}
