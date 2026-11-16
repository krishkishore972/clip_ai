package config

import (
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
)

// Redis contains connection credentials. Never log this configuration.
type Redis struct {
	Options asynq.RedisClientOpt
}

// LoadRedis is separate from Load: only queue users require REDIS_URL.
// Construction is offline; it does not establish a connection to Redis.
func LoadRedis() (Redis, error) {
	raw, err := required("REDIS_URL")
	if err != nil {
		return Redis{}, err
	}
	return parseRedis(raw)
}

func parseRedis(raw string) (Redis, error) {
	// Do not wrap URL/parser errors: they can include credentials.
	invalid := errors.New("config: REDIS_URL must be redis:// or rediss://host:port with an optional non-negative database path; query parameters and fragments are unsupported")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return Redis{}, invalid
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return Redis{}, invalid
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Redis{}, invalid
	}
	if u.Path != "" {
		db := strings.TrimPrefix(u.Path, "/")
		n, err := strconv.Atoi(db)
		if err != nil || n < 0 || strings.Contains(db, "/") {
			return Redis{}, invalid
		}
	}
	parsed, err := asynq.ParseRedisURI(raw)
	if err != nil {
		return Redis{}, invalid
	}
	opt, ok := parsed.(asynq.RedisClientOpt)
	if !ok {
		return Redis{}, invalid
	}
	// Asynq v0.26.0's URI parser preserves passwords but omits ACL usernames.
	if u.User != nil {
		opt.Username = u.User.Username()
	}
	opt.DialTimeout = 5 * time.Second
	opt.ReadTimeout = 3 * time.Second
	opt.WriteTimeout = 3 * time.Second
	if opt.TLSConfig != nil {
		opt.TLSConfig.MinVersion = tls.VersionTLS12
	}
	return Redis{Options: opt}, nil
}
