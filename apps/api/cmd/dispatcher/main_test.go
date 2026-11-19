package main

import "testing"

func TestDisabledRunDoesNotConnect(t *testing.T) {
	t.Setenv("OUTBOX_DISPATCH_ENABLED", "false")
	t.Setenv("DATABASE_URL", "invalid")
	t.Setenv("REDIS_URL", "invalid")
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsMissingConfiguration(t *testing.T) {
	t.Setenv("OUTBOX_DISPATCH_ENABLED", "")
	if err := run(); err == nil {
		t.Fatal("expected missing configuration to fail without starting")
	}
}
