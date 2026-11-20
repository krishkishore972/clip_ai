package preparation_test

import (
	"os"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/config"
)

func TestPreparationFeatureFlag(t *testing.T) {
	old, had := os.LookupEnv("MEDIA_PREPARATION_ENABLED")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("MEDIA_PREPARATION_ENABLED", old)
		} else {
			_ = os.Unsetenv("MEDIA_PREPARATION_ENABLED")
		}
	})
	_ = os.Unsetenv("MEDIA_PREPARATION_ENABLED")
	if enabled, err := config.LoadPreparationEnabled(); err != nil || enabled {
		t.Fatalf("absent flag: %t %v", enabled, err)
	}
	for _, tc := range []struct {
		raw            string
		enabled, valid bool
	}{{"true", true, true}, {"false", false, true}, {"1", true, true}, {"", false, false}, {"not-a-bool", false, false}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("MEDIA_PREPARATION_ENABLED", tc.raw)
			enabled, err := config.LoadPreparationEnabled()
			if enabled != tc.enabled || (err == nil) != tc.valid {
				t.Fatalf("flag=%t err=%v", enabled, err)
			}
		})
	}
}
