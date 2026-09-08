// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/fs"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// TestControlServerStopWaitsForHandlerUnderRealMount replays the daemon
// shutdown order (unmount -> server.Wait() -> stop()) against a real FUSE
// mount while a control handler is parked mid-request, and asserts stop()
// does not return until that handler is done with the session.
func TestControlServerStopWaitsForHandlerUnderRealMount(t *testing.T) {
	requireFUSE(t)

	lower := t.TempDir()
	stateDir := t.TempDir()

	u := upper.New()
	for _, ev := range []*kfusev1.EventEnvelope{
		{Seq: 1, SessionId: "sess1", LowerId: "low1",
			Op: &kfusev1.EventEnvelope_SessionStart{SessionStart: &kfusev1.SessionStart{LowerId: "low1"}}},
		{Seq: 2, SessionId: "sess1", LowerId: "low1",
			Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: "overlay.txt", Mode: 0o644}}},
	} {
		if err := u.Apply(ev); err != nil {
			t.Fatalf("apply event: %v", err)
		}
	}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, u, nil, nil, nil)

	m := &fs.Mounter{LowerPath: lower, Session: sess}
	server, err := m.Mount()
	if err != nil {
		if os.Getenv("KFUSE_REQUIRE_FUSE") == "1" {
			t.Fatalf("real FUSE mount unavailable: %v", err)
		}
		t.Skipf("real FUSE mount unavailable: %v", err)
	}
	mounted := true
	t.Cleanup(func() {
		if mounted {
			_ = server.Unmount()
		}
	})

	if _, err := os.Stat(filepath.Join(lower, "overlay.txt")); err != nil {
		t.Fatalf("merged view missing overlay.txt: %v", err)
	}

	stack := &Stack{Config: config.Config{StateDir: stateDir}}
	stop, err := stack.StartControlServer(sess)
	if err != nil {
		t.Fatalf("start control server: %v", err)
	}
	sock := ControlSocketPath(stateDir, "low1", "sess1")
	if _, err := ControlRequest(sock, "status"); err != nil {
		t.Fatalf("control status: %v", err)
	}

	// Park a handler mid-request: the command arrives without its newline, so
	// handleControl sits inside the session until the client finishes.
	slow, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial control socket: %v", err)
	}
	defer func() { _ = slow.Close() }()
	if _, err := slow.Write([]byte("status")); err != nil {
		t.Fatalf("write partial command: %v", err)
	}
	// The accept loop is sequential and the backlog is FIFO, so a later
	// request that gets served proves the parked one was already accepted.
	if _, err := ControlRequest(sock, "status"); err != nil {
		t.Fatalf("control status (ordering probe): %v", err)
	}

	if err := server.Unmount(); err != nil {
		t.Fatalf("unmount: %v", err)
	}
	mounted = false
	server.Wait()

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("stop() returned while the control handler was still using the session")
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := slow.Write([]byte("\n")); err != nil {
		t.Fatalf("release parked handler: %v", err)
	}
	if _, err := bufio.NewReader(slow).ReadString('\n'); err != nil {
		t.Fatalf("parked handler never replied: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop() never returned after the handler finished")
	}

	if err := m.Close(); err != nil {
		t.Fatalf("close mounter: %v", err)
	}
}
