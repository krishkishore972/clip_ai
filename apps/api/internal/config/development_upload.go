package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type DevelopmentUpload struct {
	Enabled       bool
	S3            S3
	SigningSecret string // Never log this struct or its secret.
	TokenTTL      time.Duration
}

// LoadDevelopmentUpload is opt-in. Existing blank storage settings do not break
// startup while disabled; production cannot opt into unauthenticated uploads.
func LoadDevelopmentUpload(env string) (DevelopmentUpload, error) {
	raw := strings.TrimSpace(os.Getenv("DEV_S3_UPLOAD_ENABLED"))
	if raw == "" {
		return DevelopmentUpload{}, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return DevelopmentUpload{}, errors.New("config: DEV_S3_UPLOAD_ENABLED must be a boolean")
	}
	if !enabled {
		return DevelopmentUpload{}, nil
	}
	if env != "development" {
		return DevelopmentUpload{}, errors.New("config: DEV_S3_UPLOAD_ENABLED is allowed only with API_ENV=development")
	}
	cfg := DevelopmentUpload{Enabled: true}
	var errs []error
	if cfg.S3, err = LoadS3(); err != nil {
		errs = append(errs, err)
	}
	if cfg.SigningSecret, err = required("UPLOAD_TOKEN_SECRET"); err != nil {
		errs = append(errs, err)
	} else if len(cfg.SigningSecret) < 32 {
		errs = append(errs, errors.New("config: UPLOAD_TOKEN_SECRET must contain at least 32 bytes"))
	}
	if cfg.TokenTTL, err = requiredDuration("UPLOAD_TOKEN_TTL"); err != nil {
		errs = append(errs, err)
	} else if cfg.TokenTTL < time.Minute || cfg.TokenTTL > 24*time.Hour {
		errs = append(errs, errors.New("config: UPLOAD_TOKEN_TTL must be between 1m and 24h"))
	}
	if len(errs) > 0 {
		return DevelopmentUpload{}, errors.Join(errs...)
	}
	return cfg, nil
}
