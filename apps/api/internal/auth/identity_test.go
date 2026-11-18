package auth

import (
	"context"
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
		{"empty user", WithPrincipal(context.Background(), Principal{}), false},
		{"user", WithPrincipal(context.Background(), Principal{UserID: "internal-user"}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := FromContext(tc.ctx)
			if ok != tc.want {
				t.Fatalf("authenticated principal = %t, want %t", ok, tc.want)
			}
		})
	}
}
