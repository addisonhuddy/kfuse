// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package testenv boots integration-test configuration from the .env file at
// the repo root (gitignored) or from the process environment. Never commit real
// credentials.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/config"
)

// findEnvFile locates the .env credentials file, searching from the working
// directory up to the repo root. It is gitignored; never commit it.
func findEnvFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf(".env not found above working dir (copy .env.example)")
		}
		dir = parent
	}
}

// Config returns a Config for integration tests. prefix is the S3 key prefix
// namespace for this test run, e.g. "test/<runid>/".
func Config(prefix string) (config.Config, error) {
	env := map[string]string{}
	p := "process environment"
	if found, err := findEnvFile(); err == nil {
		p = found
		raw, err := os.ReadFile(p)
		if err != nil {
			return config.Config{}, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			env[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			env[k] = v
		}
	}
	var err error
	kafkaTLS := true
	if v := env["KAFKA_TLS"]; v != "" {
		kafkaTLS, err = strconv.ParseBool(v)
		if err != nil {
			return config.Config{}, fmt.Errorf("%s KAFKA_TLS=%q is not a boolean", p, v)
		}
	}
	kafkaPartitions := 8
	if v := env["KAFKA_PARTITIONS"]; v != "" {
		kafkaPartitions, err = strconv.Atoi(v)
		if err != nil {
			return config.Config{}, fmt.Errorf("%s KAFKA_PARTITIONS=%q is not an integer", p, v)
		}
	}
	s3UsePathStyle := false
	if v := env["S3_PATH_STYLE"]; v != "" {
		s3UsePathStyle, err = strconv.ParseBool(v)
		if err != nil {
			return config.Config{}, fmt.Errorf("%s S3_PATH_STYLE=%q is not a boolean", p, v)
		}
	}
	kafkaTopic := env["KAFKA_TOPIC"]
	if kafkaTopic == "" {
		kafkaTopic = "kfuse.events"
	}
	s3Region := env["S3_REGION"]
	if s3Region == "" {
		s3Region = "us-east-1"
	}
	cfg := config.Config{
		KafkaBrokers:      env["BOOTSTRAP_SERVER"],
		KafkaSASLUsername: env["KAFKA_SASL_USERNAME"],
		KafkaSASLPassword: env["KAFKA_SASL_PASSWORD"],
		KafkaTLS:          kafkaTLS,
		KafkaTopic:        kafkaTopic,
		KafkaPartitions:   kafkaPartitions,

		S3Region:       s3Region,
		S3AccessKey:    env["S3_ACCESS_KEY"],
		S3SecretKey:    env["S3_SECRET_KEY"],
		S3Endpoint:     strings.TrimRight(env["S3_ENDPOINT"], "/"),
		S3UsePathStyle: s3UsePathStyle,

		S3Bucket: env["S3_BUCKET"],
		S3Prefix: "kfuse/" + prefix,
		LowerID:  "test-lower",
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", p, err)
	}
	return cfg, nil
}

// RunID returns a short unique identifier for one test run.
func RunID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// Require skips the test when credentials are unavailable.
func Require(t *testing.T, prefix string) config.Config {
	t.Helper()
	cfg, err := Config(prefix)
	if err != nil {
		t.Skipf("integration creds unavailable: %v", err)
	}
	return cfg
}
