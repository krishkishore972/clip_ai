package config

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

type CurationWorker struct {
	Enabled                              bool
	DatabaseURL, Region, Bucket, TempDir string
	ServiceURL, ServiceToken             string
}

func LoadCurationWorker() (CurationWorker, error) {
	enabled, err := transcriptionFlag("CURATION_WORKER_ENABLED")
	if err != nil || !enabled {
		return CurationWorker{}, err
	}
	cfg := CurationWorker{Enabled: true}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"DATABASE_URL", &cfg.DatabaseURL}, {"AWS_REGION", &cfg.Region},
		{"S3_BUCKET", &cfg.Bucket}, {"CURATION_TEMP_DIR", &cfg.TempDir},
		{"CURATION_SERVICE_URL", &cfg.ServiceURL}, {"CURATION_SERVICE_TOKEN", &cfg.ServiceToken},
	} {
		value, err := required(field.key)
		if err != nil {
			return CurationWorker{}, err
		}
		*field.dst = value
	}
	if !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) == string(filepath.Separator) ||
		strings.ContainsAny(cfg.Bucket, "/\\ \t\r\n") {
		return CurationWorker{}, errors.New("config: invalid curation bucket or private absolute temporary directory")
	}
	if err := ValidateCurationService(cfg.ServiceURL, cfg.ServiceToken); err != nil {
		return CurationWorker{}, err
	}
	return cfg, nil
}

// ValidateCurationService performs only syntax/policy checks, without DNS or
// network access. Configuration packages must not import domain services.
func ValidateCurationService(endpoint, token string) error {
	invalid := errors.New("config: invalid private curation service endpoint or token")
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Path != "/internal/curate" || u.RawPath != "" ||
		(u.Scheme != "https" && u.Scheme != "http") || len(token) < 32 || len(token) > 256 {
		return invalid
	}
	for _, ch := range token {
		if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return invalid
		}
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return invalid
		}
	}
	return nil
}
