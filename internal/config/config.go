// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates kfuse configuration from the
// environment. Canonical variable names are documented in .env.example.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	KafkaBrokers      string
	KafkaSASLUsername string
	KafkaSASLPassword string
	KafkaTLS          bool
	KafkaTopic        string
	KafkaPartitions   int

	S3Region       string
	S3AccessKey    string
	S3SecretKey    string
	S3Endpoint     string
	S3UsePathStyle bool
	S3Bucket       string
	S3Prefix       string

	LowerID  string
	StateDir string
}

const (
	defaultTopic      = "kfuse.events"
	defaultPartitions = 8
	defaultRegion     = "us-east-1"
	defaultPrefix     = "kfuse/"
)

// FromEnv builds a Config from the process environment and validates it for
// commands that need the full Kafka + S3 stack. Missing required values and
// malformed values are reported as a combined error.
func FromEnv() (Config, error) {
	cfg, loadErr := Load()
	if err := errors.Join(loadErr, cfg.Validate()); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate reports every Kafka and S3 variable a remote command needs.
func (cfg Config) Validate() error {
	var missing []string
	if cfg.KafkaBrokers == "" {
		missing = append(missing, "BOOTSTRAP_SERVER")
	}
	// Kafka can run without authentication locally, but a half-configured SASL
	// pair is always a mistake.
	if cfg.KafkaSASLUsername == "" && cfg.KafkaSASLPassword != "" {
		missing = append(missing, "KAFKA_SASL_USERNAME")
	}
	if cfg.KafkaSASLPassword == "" && cfg.KafkaSASLUsername != "" {
		missing = append(missing, "KAFKA_SASL_PASSWORD")
	}
	missing = append(missing, cfg.missingStorage()...)
	if cfg.KafkaPartitions <= 0 {
		missing = append(missing, "KAFKA_PARTITIONS (>0)")
	}
	return missingErr(missing)
}

// ValidateStorage reports the S3 variables a registry-only command (one that
// never appends to or replays the log) needs.
func (cfg Config) ValidateStorage() error {
	return missingErr(cfg.missingStorage())
}

func (cfg Config) missingStorage() []string {
	var missing []string
	if cfg.S3AccessKey == "" {
		missing = append(missing, "S3_ACCESS_KEY")
	}
	if cfg.S3SecretKey == "" {
		missing = append(missing, "S3_SECRET_KEY")
	}
	if cfg.S3Bucket == "" {
		missing = append(missing, "S3_BUCKET")
	}
	return missing
}

func missingErr(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("config: missing required env: %s", strings.Join(missing, ", "))
}

// Load reads the environment without requiring any remote variable to be set.
// Malformed values (a non-boolean KAFKA_TLS or S3_PATH_STYLE, a non-integer
// partition count, an invalid KF_LOWER_ID) and an unresolvable state dir are
// still errors: the local fields must be trustworthy for every command.
// Callers that talk to Kafka or S3 must additionally call Validate or
// ValidateStorage.
func Load() (Config, error) {
	var errs []error
	// A malformed value is never defaulted away: a mistyped partition count
	// would silently route a session's events to a different partition than
	// the replay path reads from.
	kafkaTLS, err := envBool("KAFKA_TLS", true)
	if err != nil {
		errs = append(errs, err)
	}
	kafkaPartitions, err := envInt("KAFKA_PARTITIONS", defaultPartitions)
	if err != nil {
		errs = append(errs, err)
	}
	s3UsePathStyle, err := envBool("S3_PATH_STYLE", false)
	if err != nil {
		errs = append(errs, err)
	}
	s3Endpoint := strings.TrimRight(os.Getenv("S3_ENDPOINT"), "/")
	if err := validateS3Endpoint(s3Endpoint); err != nil {
		errs = append(errs, err)
	}
	cfg := Config{
		KafkaBrokers:      os.Getenv("BOOTSTRAP_SERVER"),
		KafkaSASLUsername: os.Getenv("KAFKA_SASL_USERNAME"),
		KafkaSASLPassword: os.Getenv("KAFKA_SASL_PASSWORD"),
		KafkaTLS:          kafkaTLS,
		KafkaTopic:        envOr("KAFKA_TOPIC", defaultTopic),
		KafkaPartitions:   kafkaPartitions,

		S3Region:       envOr("S3_REGION", defaultRegion),
		S3AccessKey:    os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:    os.Getenv("S3_SECRET_KEY"),
		S3Endpoint:     s3Endpoint,
		S3UsePathStyle: s3UsePathStyle,
		S3Bucket:       os.Getenv("S3_BUCKET"),
		S3Prefix:       envOr("S3_PREFIX", defaultPrefix),

		LowerID: os.Getenv("KF_LOWER_ID"),
	}

	stateDir, err := stateDirFromEnv()
	if err != nil {
		errs = append(errs, err)
	}
	cfg.StateDir = stateDir

	if cfg.LowerID != "" {
		if err := ValidateID("KF_LOWER_ID", cfg.LowerID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}

// validateS3Endpoint accepts a direct S3-compatible endpoint such as MinIO.
// AWS's default endpoint remains implicit so the hosted path needs no extra
// setting.
func validateS3Endpoint(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("config: S3_ENDPOINT=%q must be an http:// or https:// URL", raw)
	}
	return nil
}

// stateDirFromEnv resolves the local state directory. An unresolvable home
// directory is an error: an empty state dir would scatter pidfiles, control
// sockets and session markers across the current working directory.
func stateDirFromEnv() (string, error) {
	if d := os.Getenv("KF_STATE_DIR"); d != "" {
		return d, nil
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "kfuse"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve state dir, set KF_STATE_DIR: %w", err)
	}
	return filepath.Join(h, ".kfuse"), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) (bool, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("config: %s=%q is not a boolean", k, v)
	}
	return b, nil
}

func envInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("config: %s=%q is not an integer", k, v)
	}
	return n, nil
}
