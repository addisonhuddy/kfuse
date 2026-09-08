// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// stop() must not return while a handler is still using the session: the
// caller tears the mount and the session down as soon as it returns.
func TestControlServerStopWaitsForInFlightHandler(t *testing.T) {
	s := &Stack{Config: config.Config{StateDir: t.TempDir()}}
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	stop, err := s.StartControlServer(sess)
	if err != nil {
		t.Fatal(err)
	}
	sock := ControlSocketPath(s.Config.StateDir, "low1", "sess1")

	// A client that sends its command without the terminating newline leaves
	// its handler parked inside the session for as long as it likes.
	slow, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.Close() }()
	if _, err := slow.Write([]byte("status")); err != nil {
		t.Fatal(err)
	}
	// The accept loop is sequential and the socket backlog is FIFO, so a later
	// connection that gets served proves the slow one was already accepted.
	if reply, err := ControlRequest(sock, "status"); err != nil {
		t.Fatal(err)
	} else if reply != "status sess1 0" {
		t.Fatalf("status reply = %q, want %q", reply, "status sess1 0")
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		stop()
	}()
	select {
	case <-stopped:
		t.Fatal("stop() returned while a control handler was still using the session")
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := slow.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(slow).ReadString('\n'); err != nil {
		t.Fatalf("slow handler never replied: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop() did not return after the last handler finished")
	}
}
