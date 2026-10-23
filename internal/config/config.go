// Package config loads runtime configuration from the environment. Nothing
// secret is stored here: key material comes from KMS or Secrets Manager in the
// deployed stack.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config is the Reachee runtime configuration.
type Config struct {
	Environment        string
	Region             string
	HeartMapPath       string
	SigningKeyPath     string
	EmpathyTimeout     time.Duration
	BreakerCooldown    time.Duration
	AuditTableName     string
	AuditArchiveBucket string
}

// Load reads configuration from the environment with Reachee defaults.
func Load() (Config, error) {
	c := Config{
		Environment:        env("REACHEE_ENV", "local"),
		Region:             env("AWS_REGION", "us-west-2"),
		HeartMapPath:       env("REACHEE_HEARTMAP", "examples/heartmap.yaml"),
		SigningKeyPath:     env("REACHEE_SIGNING_KEY", ""),
		EmpathyTimeout:     duration("REACHEE_EMPATHY_TIMEOUT_MS", 2000),
		BreakerCooldown:    duration("REACHEE_BREAKER_COOLDOWN_MS", 30000),
		AuditTableName:     env("REACHEE_AUDIT_TABLE", "reachee_audit_log"),
		AuditArchiveBucket: env("REACHEE_AUDIT_BUCKET", ""),
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func duration(key string, defMillis int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(defMillis) * time.Millisecond
}
