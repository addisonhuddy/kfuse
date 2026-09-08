// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package testenv boots integration-test configuration from the .env file at
// the repo root (gitignored). Never commit real credentials.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
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
	p, err := findEnvFile()
	if err != nil {
		return config.Config{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return config.Config{}, err
	}
	env := map[string]string{}
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
	cfg := config.Config{
		KafkaBrokers:      first(env["BOOTSTRAP_SERVER"], env["KF_KAFKA_BROKERS"]),
		KafkaSASLUser:     first(env["CONFLUENT_CLOUD_KEY"], env["CONFLUENT_CLOUD"], env["KF_KAFKA_SASL_USERNAME"]),
		KafkaSASLPassword: first(env["CONFLUENT_CLOUD_SECRET"], env["KF_KAFKA_SASL_PASSWORD"]),
		KafkaTLS:          true,
		KafkaTopic:        "kfuse.events",
		KafkaPartitions:   8,

		AWSRegion:    first(env["REGION"], env["AWS_REGION"]),
		AWSAccessKey: first(env["AWS_ACCESS_KEY"], env["AWS_ACCESS_KEY_ID"]),
		AWSSecretKey: first(env["AWS_SECRET_KEY"], env["AWS_SECRET_ACCESS_KEY"]),

		BlobBucket: first(env["BUCKET"], env["KF_BLOB_BUCKET"]),
		BlobPrefix: "kfuse/" + prefix,
		LowerID:    "test-lower",
	}
	var missing []string
	for k, v := range map[string]string{
		"KafkaBrokers": cfg.KafkaBrokers, "SASL user": cfg.KafkaSASLUser,
		"SASL password": cfg.KafkaSASLPassword, "region": cfg.AWSRegion,
		"access key": cfg.AWSAccessKey, "secret key": cfg.AWSSecretKey,
		"bucket": cfg.BlobBucket,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("%s missing: %v", p, missing)
	}
	return cfg, nil
}

// RunID returns a short unique identifier for one test run.
func RunID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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
