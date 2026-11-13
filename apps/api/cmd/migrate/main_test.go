package main

import (
	"io"
	"strings"
	"testing"
)

func TestRunRejectsUnsupportedCommands(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"down"}, {"reset"}, {"up", "extra"}} {
		if err := run(args, "", io.Discard); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("args %v: expected usage error", args)
		}
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	t.Parallel()
	if err := run([]string{"up"}, " ", io.Discard); err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatal("expected missing DATABASE_URL error")
	}
}

func TestRunDoesNotLeakCredentials(t *testing.T) {
	t.Parallel()
	err := run([]string{"up"}, "postgres://secret-user:secret-password@localhost:invalid-port/database", io.Discard)
	if err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatal("expected a connection failure without credentials")
	}
}
