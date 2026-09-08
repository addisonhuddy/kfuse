// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates kfuse configuration from the
// environment. Canonical variable names are documented in .env.example.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	KafkaBrokers      string
	KafkaSASLUser     string
	KafkaSASLPassword string
	KafkaTLS          bool
	KafkaTopic        string
	KafkaPartitions   int

	AWSRegion    string
	AWSAccessKey string
	AWSSecretKey string

	BlobBucket string
	BlobPrefix string

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
		missing = append(missing, "KF_KAFKA_BROKERS")
	}
	if cfg.KafkaSASLUser == "" {
		missing = append(missing, "KF_KAFKA_SASL_USERNAME")
	}
	if cfg.KafkaSASLPassword == "" {
		missing = append(missing, "KF_KAFKA_SASL_PASSWORD")
	}
	missing = append(missing, cfg.missingStorage()...)
	if cfg.KafkaPartitions <= 0 {
		missing = append(missing, "KF_KAFKA_PARTITIONS (>0)")
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
	if cfg.AWSAccessKey == "" {
		missing = append(missing, "AWS_ACCESS_KEY_ID")
	}
	if cfg.AWSSecretKey == "" {
		missing = append(missing, "AWS_SECRET_ACCESS_KEY")
	}
	if cfg.BlobBucket == "" {
		missing = append(missing, "KF_BLOB_BUCKET")
	}
	return missing
}

func missingErr(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("config: missing required env: %s", strings.Join(missing, ", "))
}

// Load reads the environment without requiring any cloud variable to be set.
// Malformed values (a non-boolean KF_KAFKA_TLS, a non-integer partition count,
// an invalid KF_LOWER_ID) and an unresolvable state dir are still errors: the
// local fields must be trustworthy for every command. Callers that talk to
// Kafka or S3 must additionally call Validate or ValidateStorage.
func Load() (Config, error) {
	var errs []error
	// A malformed value is never defaulted away: a mistyped partition count
	// would silently route a session's events to a different partition than
	// the replay path reads from.
	kafkaTLS, err := envBool("KF_KAFKA_TLS", true)
	if err != nil {
		errs = append(errs, err)
	}
	kafkaPartitions, err := envInt("KF_KAFKA_PARTITIONS", defaultPartitions)
	if err != nil {
		errs = append(errs, err)
	}
	cfg := Config{
		KafkaBrokers:      os.Getenv("KF_KAFKA_BROKERS"),
		KafkaSASLUser:     os.Getenv("KF_KAFKA_SASL_USERNAME"),
		KafkaSASLPassword: os.Getenv("KF_KAFKA_SASL_PASSWORD"),
		KafkaTLS:          kafkaTLS,
		KafkaTopic:        envOr("KF_KAFKA_TOPIC", defaultTopic),
		KafkaPartitions:   kafkaPartitions,

		AWSRegion:    envOr("AWS_REGION", defaultRegion),
		AWSAccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),

		BlobBucket: os.Getenv("KF_BLOB_BUCKET"),
		BlobPrefix: envOr("KF_BLOB_PREFIX", defaultPrefix),

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
