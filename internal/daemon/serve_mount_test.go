// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/s3fake"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// requireFUSE skips tests that need a real FUSE mount unless the environment
// insists (KFUSE_REQUIRE_FUSE=1, set by the CI job whose purpose is to run
// them), in which case a missing mount facility is a failure, not a skip.
func requireFUSE(t *testing.T) {
	t.Helper()
	fail := os.Getenv("KFUSE_REQUIRE_FUSE") == "1"
	miss := func(msg string) {
		if fail {
			t.Fatalf("KFUSE_REQUIRE_FUSE=1 but %s", msg)
		}
		t.Skip(msg)
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		miss("no /dev/fuse available")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		miss("fusermount3 not installed")
	}
}

func newServeStack(t *testing.T) (*Stack, *session.Session) {
	t.Helper()
	reg := registry.NewWithClient("test-bucket", "kfuse/", s3fake.New(t).Client())
	stack := &Stack{Config: config.Config{StateDir: t.TempDir()}, Reg: reg}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, reg)
	if err := reg.Create(context.Background(), sess.Meta); err != nil {
		t.Fatal(err)
	}
	return stack, sess
}

func isMountpoint(path string) bool {
	var st, parent syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	if err := syscall.Stat(filepath.Dir(path), &parent); err != nil {
		return false
	}
	return st.Dev != parent.Dev
}

// serveUntil runs Serve in the background and returns a channel with its
// result plus a channel closed once OnReady has fired.
func serveUntil(t *testing.T, stack *Stack, sess *session.Session, lower string) (<-chan error, <-chan struct{}) {
	t.Helper()
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- stack.Serve(context.Background(), sess, lower, ServeOptions{
			PidFile:       stack.PidFilePath(sess),
			ControlSocket: true,
			OnReady:       func() error { close(ready); return nil },
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Serve returned before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("mount never became ready")
	}
	return done, ready
}

func waitServe(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after unmount")
		return nil
	}
}

func assertReleased(t *testing.T, stack *Stack, sess *session.Session, lower string) {
	t.Helper()
	if isMountpoint(lower) {
		t.Fatal("lower still mounted after Serve returned")
	}
	if _, err := os.Stat(stack.PidFilePath(sess)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile after shutdown: %v", err)
	}
	sock := ControlSocketPath(stack.Config.StateDir, sess.LowerID(), sess.ID())
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control socket after shutdown: %v", err)
	}
	// Lease released: a fresh claim with a new token must succeed.
	if err := stack.Reg.ClaimLease(context.Background(), sess.ID(), "other", "h"); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
}

func TestServeRealMountExternalUnmount(t *testing.T) {
	requireFUSE(t)
	stack, sess := newServeStack(t)
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "lower.txt"), []byte("lower"), 0o644); err != nil {
		t.Fatal(err)
	}
	done, _ := serveUntil(t, stack, sess, lower)
	t.Cleanup(func() { _ = exec.Command("fusermount3", "-u", lower).Run() })

	if !isMountpoint(lower) {
		t.Fatal("ready reported but lower is not a mountpoint")
	}
	if _, err := os.Stat(stack.PidFilePath(sess)); err != nil {
		t.Fatalf("pidfile missing while serving: %v", err)
	}
	sock := ControlSocketPath(stack.Config.StateDir, "low1", "sess1")
	if reply, err := ControlRequest(sock, "status"); err != nil || reply != "status sess1 0" {
		t.Fatalf("control status = %q, %v", reply, err)
	}
	if b, err := os.ReadFile(filepath.Join(lower, "lower.txt")); err != nil || string(b) != "lower" {
		t.Fatalf("merged read = %q, %v", b, err)
	}
	if err := stack.Reg.ClaimLease(context.Background(), sess.ID(), "intruder", "h"); !errors.Is(err, registry.ErrLocked) {
		t.Fatalf("lease not held while serving: %v", err)
	}

	if out, err := exec.Command("fusermount3", "-u", lower).CombinedOutput(); err != nil {
		t.Fatalf("fusermount3 -u: %v: %s", err, out)
	}
	if err := waitServe(t, done); err != nil {
		t.Fatalf("Serve after external unmount: %v", err)
	}
	assertReleased(t, stack, sess, lower)
}

func TestServeRealMountSignalUnmount(t *testing.T) {
	requireFUSE(t)
	stack, sess := newServeStack(t)
	lower := t.TempDir()
	done, _ := serveUntil(t, stack, sess, lower)
	t.Cleanup(func() { _ = exec.Command("fusermount3", "-u", lower).Run() })

	// The handler is installed before OnReady fires, so the process-wide
	// SIGTERM is caught and turned into an unmount rather than killing us.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := waitServe(t, done); err != nil {
		t.Fatalf("Serve after SIGTERM: %v", err)
	}
	assertReleased(t, stack, sess, lower)
}

func TestServeRealMountReadyFailureUnmounts(t *testing.T) {
	requireFUSE(t)
	stack, sess := newServeStack(t)
	lower := t.TempDir()
	t.Cleanup(func() { _ = exec.Command("fusermount3", "-u", lower).Run() })
	want := errors.New("ready pipe gone")
	mountedAtReady := false
	err := stack.Serve(context.Background(), sess, lower, ServeOptions{
		PidFile: stack.PidFilePath(sess),
		OnReady: func() error {
			mountedAtReady = isMountpoint(lower)
			return want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("Serve err = %v, want %v", err, want)
	}
	if !mountedAtReady {
		t.Fatal("OnReady fired before the mount was live")
	}
	assertReleased(t, stack, sess, lower)
}

func TestServeRealMountRefusesSecondWriter(t *testing.T) {
	requireFUSE(t)
	stack, sess := newServeStack(t)
	lower := t.TempDir()
	done, _ := serveUntil(t, stack, sess, lower)
	t.Cleanup(func() { _ = exec.Command("fusermount3", "-u", lower).Run() })

	other := t.TempDir()
	err := stack.Serve(context.Background(), sess, other, ServeOptions{})
	if !errors.Is(err, registry.ErrLocked) {
		t.Fatalf("second Serve of a leased session: %v, want ErrLocked", err)
	}
	if isMountpoint(other) {
		t.Fatal("second writer mounted despite losing the lease")
	}

	if out, err := exec.Command("fusermount3", "-u", lower).CombinedOutput(); err != nil {
		t.Fatalf("fusermount3 -u: %v: %s", err, out)
	}
	if err := waitServe(t, done); err != nil {
		t.Fatal(err)
	}
	assertReleased(t, stack, sess, lower)
}
