package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// LoadPreparationEnabled defaults off; malformed explicit values fail closed.
func LoadPreparationEnabled() (bool, error) {
	raw, ok := os.LookupEnv("MEDIA_PREPARATION_ENABLED")
	if !ok {
		return false, nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, errors.New("config: MEDIA_PREPARATION_ENABLED must be a boolean")
	}
	return enabled, nil
}
