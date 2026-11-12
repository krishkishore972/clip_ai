package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadS3(t *testing.T) {
	t.Setenv("AWS_REGION", " ap-south-1 ")
	t.Setenv("S3_BUCKET", " private-example-bucket ")
	t.Setenv("S3_PRESIGN_TTL", "15m")
	cfg, err := LoadS3()
	if err != nil || cfg.Region != "ap-south-1" || cfg.Bucket != "private-example-bucket" || cfg.PresignTTL != 15*time.Minute {
		t.Fatalf("LoadS3() = %+v, %v", cfg, err)
	}
}

func TestLoadS3RequiresConfiguration(t *testing.T) {
	for _, key := range []string{"AWS_REGION", "S3_BUCKET", "S3_PRESIGN_TTL"} {
		t.Setenv(key, "")
	}
	_, err := LoadS3()
	if err == nil {
		t.Fatal("expected missing configuration error")
	}
	for _, key := range []string{"AWS_REGION", "S3_BUCKET", "S3_PRESIGN_TTL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("missing %s from error: %v", key, err)
		}
	}
}

func TestLoadS3RejectsInvalidTTL(t *testing.T) {
	for _, ttl := range []string{"0s", "-1m", "500ms", "16m", "forever"} {
		t.Run(ttl, func(t *testing.T) {
			t.Setenv("AWS_REGION", "ap-south-1")
			t.Setenv("S3_BUCKET", "private-example-bucket")
			t.Setenv("S3_PRESIGN_TTL", ttl)
			if _, err := LoadS3(); err == nil {
				t.Fatal("invalid TTL accepted")
			}
		})
	}
}
