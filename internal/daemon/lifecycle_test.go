// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// trace records lifecycle events in the order they happen.
type trace struct {
	mu     sync.Mutex
	events []string
}

func (tr *trace) add(ev string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.events = append(tr.events, ev)
}

func (tr *trace) list() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.events)
}

// requireOrder asserts that want appears as a subsequence of the trace.
func (tr *trace) requireOrder(t *testing.T, want ...string) {
	t.Helper()
	got := tr.list()
	i := 0
	for _, ev := range got {
		if i < len(want) && ev == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("trace order\n got: %v\nwant subsequence: %v (matched %d)", got, want, i)
	}
}

func (tr *trace) count(ev string) int {
	n := 0
	for _, e := range tr.list() {
		if e == ev {
			n++
		}
	}
	return n
}

type fakeLeases struct {
	tr        *trace
	claimErr  error
	renewErr  error
	mu        sync.Mutex
	heldToken string
}

func (f *fakeLeases) ClaimLease(_ context.Context, _, token, _ string) error {
	f.tr.add("lease.claim")
	if f.claimErr != nil {
		return f.claimErr
	}
	f.mu.Lock()
	f.heldToken = token
	f.mu.Unlock()
	return nil
}

func (f *fakeLeases) RenewLease(context.Context, string, string, string) error {
	f.tr.add("lease.renew")
	return f.renewErr
}

func (f *fakeLeases) ReleaseLease(_ context.Context, _, token string) error {
	f.tr.add("lease.release")
	f.mu.Lock()
	defer f.mu.Unlock()
	if token != f.heldToken {
		return errors.New("release with wrong token")
	}
	f.heldToken = ""
	return nil
}

// fakeMount is a mountHandle whose serving ends when the test says so (an
// "external unmount") or when Unmount is called (signal / abort).
type fakeMount struct {
	tr   *trace
	done chan struct{}
	once sync.Once
}

func newFakeMount(tr *trace) *fakeMount {
	return &fakeMount{tr: tr, done: make(chan struct{})}
}

func (f *fakeMount) externalUnmount() { f.once.Do(func() { close(f.done) }) }
func (f *fakeMount) Wait()            { <-f.done; f.tr.add("mount.wait-returned") }
func (f *fakeMount) Unmount() error {
	f.tr.add("mount.unmount")
	f.once.Do(func() { close(f.done) })
	return nil
}
func (f *fakeMount) Close() error { f.tr.add("mount.close"); return nil }

type harness struct {
	tr      *trace
	leases  *fakeLeases
	mount   *fakeMount
	lc      *lifecycle
	sig     chan<- os.Signal
	sigMu   sync.Mutex
	pidFile string
	done    chan error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tr := &trace{}
	h := &harness{tr: tr, leases: &fakeLeases{tr: tr}, mount: newFakeMount(tr), done: make(chan error, 1)}
	h.pidFile = filepath.Join(t.TempDir(), "state", "low1", "sess1", "pid")
	sess := session.NewWithState(registry.Session{ID: "sess1", LowerID: "low1"}, upper.New(), nil, nil, nil)
	h.lc = &lifecycle{
		sess:   sess,
		leases: h.leases,
		mount: func() (mountHandle, error) {
			tr.add("mount.mount")
			return h.mount, nil
		},
		control: func() (func(), error) {
			tr.add("control.start")
			return func() { tr.add("control.stop") }, nil
		},
		pidFile: h.pidFile,
		onReady: func() error {
			tr.add("ready")
			return nil
		},
		signals: func(c chan<- os.Signal) func() {
			h.sigMu.Lock()
			h.sig = c
			h.sigMu.Unlock()
			tr.add("signals.install")
			return func() { tr.add("signals.stop") }
		},
		heartbeatEvery:   5 * time.Millisecond,
		materializeEvery: 5 * time.Millisecond,
		releaseTimeout:   time.Second,
	}
	return h
}

func (h *harness) start(ctx context.Context) {
	go func() { h.done <- h.lc.run(ctx) }()
}

func (h *harness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("run did not return; trace: %v", h.tr.list())
		return nil
	}
}

// waitFor polls until the trace contains ev.
func (h *harness) waitFor(t *testing.T, ev string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.tr.count(ev) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("never saw %q; trace: %v", ev, h.tr.list())
}

func (h *harness) sendSignal() {
	h.sigMu.Lock()
	defer h.sigMu.Unlock()
	h.sig <- os.Interrupt
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestServeAcquiresAndReleasesInContractOrder(t *testing.T) {
	h := newHarness(t)
	pidAtReady := false
	inner := h.lc.onReady
	h.lc.onReady = func() error {
		pidAtReady = fileExists(h.pidFile)
		return inner()
	}
	h.start(context.Background())
	h.waitFor(t, "ready")
	if !pidAtReady {
		t.Fatal("pidfile must exist before readiness is reported")
	}
	h.waitFor(t, "lease.renew")
	h.mount.externalUnmount()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	h.tr.requireOrder(t,
		"lease.claim", "mount.mount", "control.start", "signals.install", "ready",
		"mount.wait-returned", "signals.stop", "control.stop", "mount.close", "lease.release")
	if fileExists(h.pidFile) {
		t.Fatal("pidfile left behind")
	}
	if h.tr.count("mount.unmount") != 0 {
		t.Fatalf("external unmount must not trigger our own Unmount: %v", h.tr.list())
	}
	// Heartbeats are joined before the lease is released: none may land after.
	renewsAtExit := h.tr.count("lease.renew")
	time.Sleep(30 * time.Millisecond)
	if n := h.tr.count("lease.renew"); n != renewsAtExit {
		t.Fatalf("heartbeat kept running after run returned (%d -> %d)", renewsAtExit, n)
	}
	got := h.tr.list()
	if rel := slices.Index(got, "lease.release"); slices.Contains(got[rel:], "lease.renew") {
		t.Fatalf("renew after release: %v", got)
	}
}

func TestServeSignalUnmountsThenReleases(t *testing.T) {
	h := newHarness(t)
	h.start(context.Background())
	h.waitFor(t, "ready")
	h.sendSignal()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	h.tr.requireOrder(t, "ready", "mount.unmount", "mount.wait-returned", "signals.stop", "control.stop", "mount.close", "lease.release")
}

func TestServeReadyFailureTearsDownAndReturnsError(t *testing.T) {
	h := newHarness(t)
	want := errors.New("ready pipe closed")
	h.lc.onReady = func() error { h.tr.add("ready"); return want }
	h.start(context.Background())
	if err := h.wait(t); !errors.Is(err, want) {
		t.Fatalf("run err = %v, want %v", err, want)
	}
	h.tr.requireOrder(t, "ready", "mount.unmount", "mount.wait-returned", "control.stop", "mount.close", "lease.release")
	if fileExists(h.pidFile) {
		t.Fatal("pidfile left behind")
	}
}

func TestServeStartupFailuresReleaseOnlyWhatWasAcquired(t *testing.T) {
	t.Run("lease claim fails", func(t *testing.T) {
		h := newHarness(t)
		h.leases.claimErr = registry.ErrLocked
		h.start(context.Background())
		if err := h.wait(t); !errors.Is(err, registry.ErrLocked) {
			t.Fatalf("err = %v", err)
		}
		for _, ev := range []string{"mount.mount", "lease.release", "control.start", "ready"} {
			if h.tr.count(ev) != 0 {
				t.Fatalf("%s ran after a failed claim: %v", ev, h.tr.list())
			}
		}
	})
	t.Run("mount fails", func(t *testing.T) {
		h := newHarness(t)
		h.lc.mount = func() (mountHandle, error) { h.tr.add("mount.mount"); return nil, errors.New("no /dev/fuse") }
		h.start(context.Background())
		if err := h.wait(t); err == nil || !strings.Contains(err.Error(), "no /dev/fuse") {
			t.Fatalf("err = %v", err)
		}
		h.tr.requireOrder(t, "lease.claim", "mount.mount", "lease.release")
		for _, ev := range []string{"mount.close", "control.start", "ready", "signals.install"} {
			if h.tr.count(ev) != 0 {
				t.Fatalf("%s ran after a failed mount: %v", ev, h.tr.list())
			}
		}
		if fileExists(h.pidFile) {
			t.Fatal("pidfile written for a mount that never came up")
		}
	})
	t.Run("pidfile fails", func(t *testing.T) {
		h := newHarness(t)
		blocker := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		h.lc.pidFile = filepath.Join(blocker, "pid")
		h.start(context.Background())
		if err := h.wait(t); err == nil {
			t.Fatal("pidfile failure must fail the run")
		}
		h.tr.requireOrder(t, "mount.mount", "mount.unmount", "mount.wait-returned", "mount.close", "lease.release")
		for _, ev := range []string{"control.start", "ready"} {
			if h.tr.count(ev) != 0 {
				t.Fatalf("%s ran after a failed pidfile: %v", ev, h.tr.list())
			}
		}
	})
	t.Run("control socket failure is not fatal", func(t *testing.T) {
		h := newHarness(t)
		h.lc.control = func() (func(), error) { h.tr.add("control.start"); return nil, errors.New("socket in use") }
		h.start(context.Background())
		h.waitFor(t, "ready")
		h.mount.externalUnmount()
		if err := h.wait(t); err != nil {
			t.Fatal(err)
		}
		if h.tr.count("control.stop") != 0 {
			t.Fatal("stop called for a control server that never started")
		}
	})
}

func TestServeReleaseIsIdempotentAndSignalAfterUnmountIsInert(t *testing.T) {
	h := newHarness(t)
	h.start(context.Background())
	h.waitFor(t, "ready")
	h.mount.externalUnmount()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	h.lc.release()
	h.lc.release()
	if n := h.tr.count("lease.release"); n != 1 {
		t.Fatalf("lease released %d times", n)
	}
	if n := h.tr.count("mount.close"); n != 1 {
		t.Fatalf("mount closed %d times", n)
	}
	// The handler is detached; a late signal must neither block nor unmount.
	h.lc.unmount()
	if h.tr.count("mount.unmount") != 0 {
		t.Fatal("Unmount issued after serving already stopped")
	}
}

func TestServeCancelledContextDoesNotUnmount(t *testing.T) {
	// Context cancellation stops background work but a live mount stays live
	// until unmounted: only the kernel/signal path ends serving.
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.start(ctx)
	h.waitFor(t, "ready")
	cancel()
	select {
	case err := <-h.done:
		t.Fatalf("run returned on ctx cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	h.mount.externalUnmount()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
}

// Real control server under the lifecycle: concurrent requests during
// serving all get answered, and shutdown waits for them before the session's
// resources go away.
func TestServeDrainsConcurrentControlRequests(t *testing.T) {
	h := newHarness(t)
	stateDir := t.TempDir()
	s := &Stack{Config: config.Config{StateDir: stateDir}}
	sess := h.lc.sess
	h.lc.control = func() (func(), error) {
		h.tr.add("control.start")
		stop, err := s.StartControlServer(sess)
		if err != nil {
			return nil, err
		}
		return func() { stop(); h.tr.add("control.stop") }, nil
	}
	h.start(context.Background())
	h.waitFor(t, "ready")
	sock := ControlSocketPath(stateDir, "low1", "sess1")

	const n = 16
	var wg sync.WaitGroup
	replies := make(chan string, 2*n)
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r, err := ControlRequest(sock, "status")
			if err != nil {
				errs <- err
				return
			}
			replies <- r
		}()
		go func() {
			defer wg.Done()
			r, err := ControlRequest(sock, "checkpoint")
			if err != nil {
				errs <- err
				return
			}
			replies <- r
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("control request during serving: %v", err)
	}
	close(replies)
	got := 0
	for r := range replies {
		got++
		if r != "status sess1 0" && r != "offset -1" {
			t.Fatalf("unexpected reply %q", r)
		}
	}
	if got != 2*n {
		t.Fatalf("%d replies, want %d", got, 2*n)
	}
	h.mount.externalUnmount()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	h.tr.requireOrder(t, "mount.wait-returned", "control.stop", "mount.close", "lease.release")
	if fileExists(sock) {
		t.Fatal("control socket left behind")
	}
	if _, err := ControlRequest(sock, "status"); err == nil {
		t.Fatal("control socket still answering after shutdown")
	}
}

func TestServeHeartbeatLossFencesSession(t *testing.T) {
	h := newHarness(t)
	h.leases.renewErr = registry.ErrLocked
	h.start(context.Background())
	h.waitFor(t, "lease.renew")
	deadline := time.Now().Add(2 * time.Second)
	for !h.lc.sess.LeaseLost() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !h.lc.sess.LeaseLost() {
		t.Fatal("session not fenced after lease renew reported ErrLocked")
	}
	h.mount.externalUnmount()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
}
