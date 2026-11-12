package config

import (
	"errors"
	"time"
)

// S3 is non-secret application configuration. Credentials are resolved by the
// AWS SDK default chain (environment, CLI profiles/SSO, or workload IAM role).
type S3 struct {
	Region     string
	Bucket     string
	PresignTTL time.Duration
}

// LoadS3 is separate from Load so empty AWS placeholders do not prevent the
// existing health and handler-only development server from starting.
// Call it when constructing a real upload service, not for every request.
func LoadS3() (S3, error) {
	var cfg S3
	var errs []error
	var err error
	if cfg.Region, err = required("AWS_REGION"); err != nil {
		errs = append(errs, err)
	}
	if cfg.Bucket, err = required("S3_BUCKET"); err != nil {
		errs = append(errs, err)
	}
	if cfg.PresignTTL, err = requiredDuration("S3_PRESIGN_TTL"); err != nil {
		errs = append(errs, err)
	} else if cfg.PresignTTL < time.Second || cfg.PresignTTL > 15*time.Minute {
		errs = append(errs, errors.New("config: S3_PRESIGN_TTL must be between 1s and 15m"))
	}
	if len(errs) > 0 {
		return S3{}, errors.Join(errs...)
	}
	return cfg, nil
}
