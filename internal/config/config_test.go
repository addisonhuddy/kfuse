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
	"BOOTSTRAP_SERVER": "broker:9092",
	"S3_ACCESS_KEY":    "akid",
	"S3_SECRET_KEY":    "secret",
	"S3_BUCKET":        "bucket",
}

// clearEnv unsets every variable FromEnv reads so a test starts from a known
// state regardless of the ambient environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BOOTSTRAP_SERVER", "KAFKA_SASL_USERNAME", "KAFKA_SASL_PASSWORD",
		"KAFKA_TLS", "KAFKA_TOPIC", "KAFKA_PARTITIONS",
		"S3_REGION", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_ENDPOINT",
		"S3_PATH_STYLE", "S3_BUCKET", "S3_PREFIX", "KF_LOWER_ID",
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
	if cfg.S3Region != defaultRegion {
		t.Errorf("S3Region = %q, want %q", cfg.S3Region, defaultRegion)
	}
	if cfg.S3Prefix != defaultPrefix {
		t.Errorf("S3Prefix = %q, want %q", cfg.S3Prefix, defaultPrefix)
	}
	if !cfg.KafkaTLS {
		t.Error("KafkaTLS should default to true")
	}
	if cfg.S3Endpoint != "" || cfg.S3UsePathStyle {
		t.Errorf("S3 endpoint/path style = %q/%v, want AWS defaults", cfg.S3Endpoint, cfg.S3UsePathStyle)
	}
}

func TestFromEnvAllowsUnauthenticatedKafka(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv without Kafka SASL credentials: %v", err)
	}
	if cfg.KafkaSASLUsername != "" || cfg.KafkaSASLPassword != "" {
		t.Errorf("Kafka SASL credentials = %q/%q, want empty", cfg.KafkaSASLUsername, cfg.KafkaSASLPassword)
	}
}

func TestFromEnvRejectsHalfConfiguredKafkaSASL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setKey  string
		missing string
	}{
		{name: "username only", setKey: "KAFKA_SASL_USERNAME", missing: "KAFKA_SASL_PASSWORD"},
		{name: "password only", setKey: "KAFKA_SASL_PASSWORD", missing: "KAFKA_SASL_USERNAME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			setRequired(t)
			t.Setenv(tc.setKey, "set")

			_, err := FromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("FromEnv with only %s = %v, want an error naming %s", tc.setKey, err, tc.missing)
			}
		})
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("KAFKA_TOPIC", "custom.events")
	t.Setenv("KAFKA_PARTITIONS", "3")
	t.Setenv("KAFKA_TLS", "true")
	t.Setenv("KAFKA_SASL_USERNAME", "user")
	t.Setenv("KAFKA_SASL_PASSWORD", "pass")
	t.Setenv("S3_REGION", "eu-west-2")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_PATH_STYLE", "true")
	t.Setenv("S3_PREFIX", "custom/")
	t.Setenv("KF_LOWER_ID", "lower-42")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	want := Config{
		KafkaBrokers:      "broker:9092",
		KafkaSASLUsername: "user",
		KafkaSASLPassword: "pass",
		KafkaTLS:          true,
		KafkaTopic:        "custom.events",
		KafkaPartitions:   3,
		S3Region:          "eu-west-2",
		S3AccessKey:       "akid",
		S3SecretKey:       "secret",
		S3Endpoint:        "http://localhost:9000",
		S3UsePathStyle:    true,
		S3Bucket:          "bucket",
		S3Prefix:          "custom/",
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
		{"KAFKA_TLS", "yolo"},
		{"KAFKA_PARTITIONS", "not-a-number"},
		{"S3_PATH_STYLE", "maybe"},
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
	t.Setenv("S3_BUCKET", "")
	t.Setenv("KAFKA_TLS", "maybe")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("FromEnv must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "KAFKA_TLS") || !strings.Contains(msg, "S3_BUCKET") {
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
	t.Setenv("KAFKA_PARTITIONS", "0")

	_, err := FromEnv()
	if err == nil || !strings.Contains(err.Error(), "KAFKA_PARTITIONS") {
		t.Fatalf("FromEnv with 0 partitions = %v, want a KAFKA_PARTITIONS error", err)
	}
}

func TestFromEnvRejectsInvalidS3Endpoint(t *testing.T) {
	for _, endpoint := range []string{"localhost:9000", "ftp://minio:9000", "http://"} {
		t.Run(endpoint, func(t *testing.T) {
			clearEnv(t)
			setRequired(t)
			t.Setenv("S3_ENDPOINT", endpoint)

			_, err := FromEnv()
			if err == nil || !strings.Contains(err.Error(), "S3_ENDPOINT") {
				t.Fatalf("FromEnv with S3_ENDPOINT=%q = %v, want an endpoint error", endpoint, err)
			}
		})
	}
}

func TestFromEnvNormalizesS3Endpoint(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("S3_ENDPOINT", "http://localhost:9000/")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.S3Endpoint != "http://localhost:9000" {
		t.Fatalf("S3Endpoint = %q, want trailing slash removed", cfg.S3Endpoint)
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

	if got := envOr("KAFKA_TOPIC", "def"); got != "def" {
		t.Errorf("envOr unset = %q, want %q", got, "def")
	}
	t.Setenv("KAFKA_TOPIC", "set")
	if got := envOr("KAFKA_TOPIC", "def"); got != "set" {
		t.Errorf("envOr set = %q, want %q", got, "set")
	}

	if got, err := envBool("KAFKA_TLS", true); err != nil || !got {
		t.Errorf("envBool unset = (%v, %v), want the default", got, err)
	}
	t.Setenv("KAFKA_TLS", "0")
	if got, err := envBool("KAFKA_TLS", true); err != nil || got {
		t.Errorf(`envBool("0") = (%v, %v), want false`, got, err)
	}
	t.Setenv("KAFKA_TLS", "nope")
	if _, err := envBool("KAFKA_TLS", true); err == nil {
		t.Error(`envBool("nope") must fail`)
	}

	if got, err := envInt("KAFKA_PARTITIONS", 7); err != nil || got != 7 {
		t.Errorf("envInt unset = (%d, %v), want 7", got, err)
	}
	t.Setenv("KAFKA_PARTITIONS", "12")
	if got, err := envInt("KAFKA_PARTITIONS", 7); err != nil || got != 12 {
		t.Errorf("envInt = (%d, %v), want 12", got, err)
	}
	t.Setenv("KAFKA_PARTITIONS", "twelve")
	if _, err := envInt("KAFKA_PARTITIONS", 7); err == nil {
		t.Error(`envInt("twelve") must fail`)
	}
}
