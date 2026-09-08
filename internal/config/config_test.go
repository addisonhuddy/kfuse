// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// requiredEnv is the minimum set that makes FromEnv succeed.
var requiredEnv = map[string]string{
	"KF_KAFKA_BROKERS":       "broker:9092",
	"KF_KAFKA_SASL_USERNAME": "user",
	"KF_KAFKA_SASL_PASSWORD": "pass",
	"AWS_ACCESS_KEY_ID":      "akid",
	"AWS_SECRET_ACCESS_KEY":  "secret",
	"KF_BLOB_BUCKET":         "bucket",
}

// clearEnv unsets every variable FromEnv reads so a test starts from a known
// state regardless of the ambient environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"KF_KAFKA_BROKERS", "KF_KAFKA_SASL_USERNAME", "KF_KAFKA_SASL_PASSWORD",
		"KF_KAFKA_TLS", "KF_KAFKA_TOPIC", "KF_KAFKA_PARTITIONS",
		"AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"KF_BLOB_BUCKET", "KF_BLOB_PREFIX", "KF_LOWER_ID",
		"KF_STATE_DIR", "XDG_STATE_HOME",
	} {
		t.Setenv(k, "")
	}
}

func setRequired(t *testing.T) {
	t.Helper()
	for k, v := range requiredEnv {
		t.Setenv(k, v)
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.KafkaTopic != defaultTopic {
		t.Errorf("KafkaTopic = %q, want %q", cfg.KafkaTopic, defaultTopic)
	}
	if cfg.KafkaPartitions != defaultPartitions {
		t.Errorf("KafkaPartitions = %d, want %d", cfg.KafkaPartitions, defaultPartitions)
	}
	if cfg.AWSRegion != defaultRegion {
		t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, defaultRegion)
	}
	if cfg.BlobPrefix != defaultPrefix {
		t.Errorf("BlobPrefix = %q, want %q", cfg.BlobPrefix, defaultPrefix)
	}
	if !cfg.KafkaTLS {
		t.Error("KafkaTLS should default to true")
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("KF_KAFKA_TOPIC", "custom.events")
	t.Setenv("KF_KAFKA_PARTITIONS", "3")
	t.Setenv("KF_KAFKA_TLS", "true")
	t.Setenv("AWS_REGION", "eu-west-2")
	t.Setenv("KF_BLOB_PREFIX", "custom/")
	t.Setenv("KF_LOWER_ID", "lower-42")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	want := Config{
		KafkaBrokers:      "broker:9092",
		KafkaSASLUser:     "user",
		KafkaSASLPassword: "pass",
		KafkaTLS:          true,
		KafkaTopic:        "custom.events",
		KafkaPartitions:   3,
		AWSRegion:         "eu-west-2",
		AWSAccessKey:      "akid",
		AWSSecretKey:      "secret",
		BlobBucket:        "bucket",
		BlobPrefix:        "custom/",
		LowerID:           "lower-42",
		StateDir:          cfg.StateDir,
	}
	if cfg != want {
		t.Errorf("FromEnv() = %+v, want %+v", cfg, want)
	}
}

// A malformed numeric/boolean value fails the load: silently defaulting a
// typo'd partition count routes events to a partition replay never reads.
func TestFromEnvRejectsMalformedValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"KF_KAFKA_TLS", "yolo"},
		{"KF_KAFKA_PARTITIONS", "not-a-number"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			clearEnv(t)
			setRequired(t)
			t.Setenv(tc.key, tc.value)

			_, err := FromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("FromEnv with %s=%q = %v, want an error naming the variable", tc.key, tc.value, err)
			}
		})
	}
}

// Malformed and missing values are collected, so one load reports everything
// that needs fixing.
func TestFromEnvReportsMalformedAndMissingTogether(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("KF_BLOB_BUCKET", "")
	t.Setenv("KF_KAFKA_TLS", "maybe")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("FromEnv must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "KF_KAFKA_TLS") || !strings.Contains(msg, "KF_BLOB_BUCKET") {
		t.Fatalf("want both problems reported, got: %v", err)
	}
}

func TestFromEnvReportsEveryMissingRequiredVar(t *testing.T) {
	clearEnv(t)

	_, err := FromEnv()
	if err == nil {
		t.Fatal("FromEnv with empty environment must fail")
	}
	for k := range requiredEnv {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error %q does not mention missing %s", err, k)
		}
	}
}

func TestFromEnvRejectsNonPositivePartitions(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("KF_KAFKA_PARTITIONS", "0")

	_, err := FromEnv()
	if err == nil || !strings.Contains(err.Error(), "KF_KAFKA_PARTITIONS") {
		t.Fatalf("FromEnv with 0 partitions = %v, want a KF_KAFKA_PARTITIONS error", err)
	}
}

func TestFromEnvStateDirPrecedence(t *testing.T) {
	explicit := t.TempDir()
	xdg := t.TempDir()

	t.Run("explicit wins", func(t *testing.T) {
		clearEnv(t)
		setRequired(t)
		t.Setenv("KF_STATE_DIR", explicit)
		t.Setenv("XDG_STATE_HOME", xdg)

		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.StateDir != explicit {
			t.Errorf("StateDir = %q, want %q", cfg.StateDir, explicit)
		}
	})

	t.Run("xdg is next", func(t *testing.T) {
		clearEnv(t)
		setRequired(t)
		t.Setenv("XDG_STATE_HOME", xdg)

		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(xdg, "kfuse"); cfg.StateDir != want {
			t.Errorf("StateDir = %q, want %q", cfg.StateDir, want)
		}
	})

	t.Run("home fallback", func(t *testing.T) {
		clearEnv(t)
		setRequired(t)
		home := t.TempDir()
		t.Setenv("HOME", home)

		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".kfuse"); cfg.StateDir != want {
			t.Errorf("StateDir = %q, want %q", cfg.StateDir, want)
		}
	})
}

func TestEnvHelpers(t *testing.T) {
	clearEnv(t)

	if got := envOr("KF_KAFKA_TOPIC", "def"); got != "def" {
		t.Errorf("envOr unset = %q, want %q", got, "def")
	}
	t.Setenv("KF_KAFKA_TOPIC", "set")
	if got := envOr("KF_KAFKA_TOPIC", "def"); got != "set" {
		t.Errorf("envOr set = %q, want %q", got, "set")
	}

	if got, err := envBool("KF_KAFKA_TLS", true); err != nil || !got {
		t.Errorf("envBool unset = (%v, %v), want the default", got, err)
	}
	t.Setenv("KF_KAFKA_TLS", "0")
	if got, err := envBool("KF_KAFKA_TLS", true); err != nil || got {
		t.Errorf(`envBool("0") = (%v, %v), want false`, got, err)
	}
	t.Setenv("KF_KAFKA_TLS", "nope")
	if _, err := envBool("KF_KAFKA_TLS", true); err == nil {
		t.Error(`envBool("nope") must fail`)
	}

	if got, err := envInt("KF_KAFKA_PARTITIONS", 7); err != nil || got != 7 {
		t.Errorf("envInt unset = (%d, %v), want 7", got, err)
	}
	t.Setenv("KF_KAFKA_PARTITIONS", "12")
	if got, err := envInt("KF_KAFKA_PARTITIONS", 7); err != nil || got != 12 {
		t.Errorf("envInt = (%d, %v), want 12", got, err)
	}
	t.Setenv("KF_KAFKA_PARTITIONS", "twelve")
	if _, err := envInt("KF_KAFKA_PARTITIONS", 7); err == nil {
		t.Error(`envInt("twelve") must fail`)
	}
}
