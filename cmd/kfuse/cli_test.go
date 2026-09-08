// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/daemon"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// cloudEnv is every variable a remote command needs. Local tests clear all of
// them so any attempt to build a Kafka or S3 client fails validation loudly.
var cloudEnv = []string{
	"KF_KAFKA_BROKERS", "KF_KAFKA_SASL_USERNAME", "KF_KAFKA_SASL_PASSWORD",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "KF_BLOB_BUCKET",
	"AWS_REGION", "KF_KAFKA_TLS", "KF_KAFKA_TOPIC", "KF_KAFKA_PARTITIONS",
}

// localOnly gives the test a temp state dir and lower with no cloud variables
// set, returning the state dir and lower path.
func localOnly(t *testing.T) (stateDir, lowerPath string) {
	t.Helper()
	for _, k := range cloudEnv {
		t.Setenv(k, "")
	}
	stateDir = t.TempDir()
	lowerPath = t.TempDir()
	t.Setenv("KF_STATE_DIR", stateDir)
	t.Setenv("KF_LOWER_ID", "")
	return stateDir, lowerPath
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestSelectWorksWithoutCloudCredentials(t *testing.T) {
	stateDir, lower := localOnly(t)
	out, err := run(t, "session", "select", "abc123", "--lower", lower, "--lower-id", "low1")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if out != "abc123\n" {
		t.Fatalf("select output = %q, want the session id", out)
	}
	got, err := daemon.ReadSelected(stateDir, "low1")
	if err != nil || got != "abc123" {
		t.Fatalf("ReadSelected = (%q, %v), want abc123", got, err)
	}
}

// Lower-ID precedence is unchanged: flag > KF_LOWER_ID > marker > minted.
func TestLowerIDPrecedenceIsPreserved(t *testing.T) {
	stateDir, lower := localOnly(t)
	if err := os.WriteFile(filepath.Join(lower, ".kf-lower-id"), []byte("from-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "session", "select", "s1", "--lower", lower); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(daemon.SelectedPath(stateDir, "from-marker")); err != nil {
		t.Fatalf("marker id not used: %v", err)
	}
	t.Setenv("KF_LOWER_ID", "from-env")
	if _, err := run(t, "session", "select", "s2", "--lower", lower); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(daemon.SelectedPath(stateDir, "from-env")); err != nil {
		t.Fatalf("env id not preferred over marker: %v", err)
	}
	if _, err := run(t, "session", "select", "s3", "--lower", lower, "--lower-id", "from-flag"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(daemon.SelectedPath(stateDir, "from-flag")); err != nil {
		t.Fatalf("flag id not preferred over env: %v", err)
	}
}

func TestStatusAndUmountReportNoMountWithoutCloudCredentials(t *testing.T) {
	_, lower := localOnly(t)
	for _, cmd := range []string{"status", "umount"} {
		_, err := run(t, cmd, "--lower", lower, "--lower-id", "low1")
		if err == nil {
			t.Fatalf("%s with no daemon must fail", cmd)
		}
		if strings.Contains(err.Error(), "missing required env") {
			t.Fatalf("%s demanded cloud credentials for a local operation: %v", cmd, err)
		}
		if !strings.Contains(err.Error(), "no mount for lower") {
			t.Fatalf("%s error = %v, want the local no-mount error", cmd, err)
		}
	}
}

// Malformed local configuration is still refused by local commands.
func TestLocalCommandsStillRejectMalformedEnv(t *testing.T) {
	_, lower := localOnly(t)
	t.Setenv("KF_LOWER_ID", "bad id with spaces")
	if _, err := run(t, "session", "select", "s", "--lower", lower); err == nil || !strings.Contains(err.Error(), "KF_LOWER_ID") {
		t.Fatalf("select with an invalid KF_LOWER_ID = %v, want a validation error", err)
	}
}

// startLocalDaemon fakes a live mounted daemon for this process: a pidfile
// with our own pid and a control server over a credential-free session.
func startLocalDaemon(t *testing.T, stateDir string) (sessID string) {
	t.Helper()
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	stack := &daemon.Stack{Config: config.Config{StateDir: stateDir}}
	if err := daemon.WritePidFile(stack.PidFilePath(sess), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	stop, err := stack.StartControlServer(sess)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return sess.ID()
}

func TestStatusUsesControlPathWithoutCloudCredentials(t *testing.T) {
	stateDir, lower := localOnly(t)
	sessID := startLocalDaemon(t, stateDir)
	out, err := run(t, "status", "--lower", lower, "--lower-id", "low1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := "mounted: session " + sessID + " pid " + strconv.Itoa(os.Getpid()) + " seq 0\n"
	if out != want {
		t.Fatalf("status output = %q, want %q", out, want)
	}
}

// With a live daemon, checkpoint is answered over the control socket and never
// reaches the (unconfigured) cloud stack.
func TestCheckpointUsesDaemonWithoutCloudCredentials(t *testing.T) {
	stateDir, lower := localOnly(t)
	startLocalDaemon(t, stateDir)
	out, err := run(t, "checkpoint", "--lower", lower, "--lower-id", "low1")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// A session that never appended has nothing durable to snapshot.
	if out != "-1\n" {
		t.Fatalf("checkpoint output = %q, want -1", out)
	}
}

// Without a daemon, an explicit session id needs the log-based path, and that
// path still validates the full remote configuration.
func TestCheckpointFallbackStillRequiresCloudConfig(t *testing.T) {
	_, lower := localOnly(t)
	_, err := run(t, "checkpoint", "somesession", "--lower", lower, "--lower-id", "low1")
	if err == nil || !strings.Contains(err.Error(), "missing required env") {
		t.Fatalf("checkpoint fallback = %v, want strict config validation", err)
	}
	for _, v := range []string{"KF_KAFKA_BROKERS", "AWS_ACCESS_KEY_ID", "KF_BLOB_BUCKET"} {
		if !strings.Contains(err.Error(), v) {
			t.Errorf("error does not name %s: %v", v, err)
		}
	}
}

func TestRemoteCommandsValidateBeforeDialing(t *testing.T) {
	_, lower := localOnly(t)
	for _, args := range [][]string{
		{"session", "new"},
		{"session", "branch", "parent"},
		{"mount", "sess"},
	} {
		_, err := run(t, append(args, "--lower", lower, "--lower-id", "low1")...)
		if err == nil || !strings.Contains(err.Error(), "missing required env") {
			t.Errorf("%v = %v, want strict config validation", args, err)
		}
	}
}

// session ls only reads the registry, so it must ask for the S3 variables and
// nothing about Kafka.
func TestSessionLsRequiresOnlyStorageConfig(t *testing.T) {
	_, lower := localOnly(t)
	_, err := run(t, "session", "ls", "--lower", lower, "--lower-id", "low1")
	if err == nil {
		t.Fatal("session ls without storage config must fail")
	}
	msg := err.Error()
	for _, v := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "KF_BLOB_BUCKET"} {
		if !strings.Contains(msg, v) {
			t.Errorf("session ls error does not name %s: %v", v, err)
		}
	}
	if strings.Contains(msg, "KAFKA") {
		t.Fatalf("session ls demanded Kafka configuration: %v", err)
	}
}

func TestVersionWritesToCommandWriter(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if out != version+"\n" {
		t.Fatalf("version output = %q", out)
	}
}

// Two independently built command trees must not share flag state.
func TestCommandTreesAreIndependent(t *testing.T) {
	a, b := newRootCmd(), newRootCmd()
	a.SetOut(io.Discard)
	a.SetArgs([]string{"mount", "--lower", "/a", "--foreground", "--help"})
	if err := a.Execute(); err != nil {
		t.Fatal(err)
	}
	mountB, _, err := b.Find([]string{"mount"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mountB.Flags().GetBool("foreground"); v {
		t.Fatal("--foreground leaked from one command tree into another")
	}
	if v, _ := mountB.PersistentFlags().GetString("lower"); v != "." {
		t.Fatalf("--lower leaked across trees: %q", v)
	}
}
