package config

import (
	"crypto/tls"
	"strings"
	"testing"
)

func TestLoadRedis(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	if _, err := LoadRedis(); err == nil || !strings.Contains(err.Error(), "REDIS_URL") {
		t.Fatal("expected required REDIS_URL error")
	}
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")
	if _, err := LoadRedis(); err != nil {
		t.Fatal(err)
	}
}

func TestParseRedis(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw, addr, username, password string
		db                                  int
		tls                                 bool
	}{
		{"local", "redis://127.0.0.1:6379/0", "127.0.0.1:6379", "", "", 0, false},
		{"no database", "redis://localhost:6379", "localhost:6379", "", "", 0, false},
		{"password", "redis://:fake-password@localhost:6379/2", "localhost:6379", "", "fake-password", 2, false},
		{"ACL and TLS", "rediss://worker:fake%40password@redis.example.invalid:6380/1", "redis.example.invalid:6380", "worker", "fake@password", 1, true},
		{"IPv6", "redis://[::1]:6379/0", "[::1]:6379", "", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseRedis(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			opt := cfg.Options
			if opt.Addr != tc.addr || opt.Username != tc.username || opt.Password != tc.password || opt.DB != tc.db {
				t.Fatal("connection fields do not match expected values")
			}
			if (opt.TLSConfig != nil) != tc.tls {
				t.Fatal("unexpected TLS configuration")
			}
			if tc.tls && (opt.TLSConfig.InsecureSkipVerify || opt.TLSConfig.MinVersion < tls.VersionTLS12 || opt.TLSConfig.ServerName != "redis.example.invalid") {
				t.Fatal("TLS must verify server identity with TLS 1.2 or newer")
			}
			if opt.DialTimeout <= 0 || opt.ReadTimeout <= 0 || opt.WriteTimeout <= 0 {
				t.Fatal("timeouts must be bounded")
			}
		})
	}
}

func TestParseRedisRejectsInvalidWithoutSecrets(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"", "https://localhost:6379", "redis://", "redis://localhost", "redis://localhost:0", "redis://localhost:65536",
		"redis://localhost:6379/-1", "redis://localhost:6379/not-a-db", "redis://localhost:6379/0/extra", "redis://localhost:6379/",
		"redis://localhost:6379/0?tls=false", "redis://localhost:6379/0#fragment", "redis://localhost:6379/0?",
		"redis://:fake-secret@localhost:bad", "redis://:fake-secret%zz@localhost:6379/0", "redis-socket:///tmp/redis.sock",
	} {
		_, err := parseRedis(raw)
		if err == nil {
			t.Fatal("expected invalid Redis configuration to be rejected")
		}
		if strings.Contains(err.Error(), "fake-secret") {
			t.Fatal("error exposed credentials")
		}
	}
}
