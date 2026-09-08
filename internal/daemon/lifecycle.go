// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/addisonhuddy/kfuse/internal/fs"
	klog "github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/perf"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
)

// ServeOptions configures the side resources a served mount owns besides the
// lease and the FUSE server itself.
type ServeOptions struct {
	// PidFile, when non-empty, is written with this process's pid once the
	// mount is live and removed on shutdown.
	PidFile string
	// ControlSocket serves `kfuse status`/`kfuse checkpoint` requests against
	// the live session. Failure to open it is logged, not fatal.
	ControlSocket bool
	// OnReady runs once the mount is live and every other resource is up. A
	// non-nil error tears the mount down and is returned from Serve.
	OnReady func() error
}

// Serve mounts sess over lowerPath and blocks until the mount is gone.
//
// Serve is the single owner of a mount's resources and defines their order:
//
//	acquire:  lease -> heartbeat + periodic checkpoint goroutines -> FUSE mount
//	          -> pidfile -> control socket -> signal handler -> OnReady
//	release:  signal handler -> control socket (in-flight handlers finish)
//	          -> heartbeat/periodic checkpoint (goroutines joined)
//	          -> commit-triggered checkpoints (drained)
//	          -> lower directory fd -> pidfile -> lease
//
// Release runs in exactly that order after FUSE serving has stopped, whether
// the mount ended by SIGINT/SIGTERM, an external unmount, an OnReady error,
// or a failure part-way through acquisition (in which case only the
// resources acquired so far are released). FUSE serving stops -- and every
// in-flight FUSE request with it -- before anything that request could touch
// (the session, the lower fd, the lease) goes away. Nothing implicit happens
// on unmount: uncheckpointed state stays only in the log, as before.
func (s *Stack) Serve(ctx context.Context, sess *session.Session, lowerPath string, opts ServeOptions) error {
	lc := &lifecycle{
		sess:   sess,
		leases: s.Reg,
		mount: func() (mountHandle, error) {
			m := &fs.Mounter{LowerPath: lowerPath, Session: sess, Blobs: s.Blobs}
			server, err := m.Mount()
			if err != nil {
				return nil, err
			}
			return &fuseMount{server: server, mounter: m}, nil
		},
		pidFile: opts.PidFile,
		onReady: opts.OnReady,
	}
	if opts.ControlSocket {
		lc.control = func() (func(), error) { return s.StartControlServer(sess) }
	}
	return lc.run(ctx)
}

// Mount serves a session over the lower path with no pidfile or control
// socket. It claims the writer lease, heartbeats it while mounted, and
// releases it on unmount. Blocks until unmounted.
func (s *Stack) Mount(ctx context.Context, sess *session.Session, lowerPath string, onReady func()) error {
	var ready func() error
	if onReady != nil {
		ready = func() error { onReady(); return nil }
	}
	return s.Serve(ctx, sess, lowerPath, ServeOptions{OnReady: ready})
}

// leaseStore is the registry surface the lifecycle needs.
type leaseStore interface {
	ClaimLease(ctx context.Context, id, token, host string) error
	RenewLease(ctx context.Context, id, token, host string) error
	ReleaseLease(ctx context.Context, id, token string) error
}

// mountHandle is a live FUSE mount. Wait returns once serving has stopped and
// every in-flight request has been answered; Close releases what the mount
// held open (the lower directory fd) and must only run after Wait.
type mountHandle interface {
	Wait()
	Unmount() error
	Close() error
}

type fuseMount struct {
	server interface {
		Wait()
		Unmount() error
	}
	mounter *fs.Mounter
}

func (f *fuseMount) Wait()          { f.server.Wait() }
func (f *fuseMount) Unmount() error { return f.server.Unmount() }
func (f *fuseMount) Close() error   { return f.mounter.Close() }

// lifecycle is one Serve run. Every resource it acquires is recorded on the
// struct so release can run once, in order, from any exit path.
type lifecycle struct {
	sess    *session.Session
	leases  leaseStore
	mount   func() (mountHandle, error)
	control func() (stop func(), err error)
	pidFile string
	onReady func() error

	// signals is the delivery hook; nil means os/signal for SIGINT/SIGTERM.
	signals func(c chan<- os.Signal) (stop func())
	// materializeEvery overrides materializeInterval (tests).
	materializeEvery time.Duration
	// heartbeatEvery overrides registry.LeaseRefresh (tests).
	heartbeatEvery time.Duration
	// releaseTimeout overrides leaseReleaseTimeout (tests).
	releaseTimeout time.Duration

	token, host string

	// Acquired resources, in acquisition order.
	bgCancel   context.CancelFunc
	bg         sync.WaitGroup
	handle     mountHandle
	pidWritten bool
	stopCtl    func()
	stopSig    func()

	served      atomic.Bool
	unmountOnce sync.Once
	releaseOnce sync.Once
}

func (lc *lifecycle) run(ctx context.Context) (err error) {
	defer func() {
		// Release is defined for any partial acquisition, so one deferred
		// call covers every early return below as well as normal shutdown.
		lc.release()
	}()

	lc.token, err = session.NewID()
	if err != nil {
		return err
	}
	lc.host, err = os.Hostname()
	if err != nil {
		klog.Warn("hostname lookup failed; using fallback", klog.Err(err), "host", "unknown-host")
		lc.host = "unknown-host"
	}
	if err := lc.leases.ClaimLease(ctx, lc.sess.ID(), lc.token, lc.host); err != nil {
		lc.token = "" // nothing to release
		return fmt.Errorf("kfuse: %w", err)
	}

	bgCtx, cancel := context.WithCancel(ctx)
	lc.bgCancel = cancel
	lc.bg.Add(2)
	go func() { defer lc.bg.Done(); lc.runLeaseHeartbeat(bgCtx) }()
	go func() { defer lc.bg.Done(); lc.runMaterializer(bgCtx) }()

	lc.handle, err = lc.mount()
	if err != nil {
		return err
	}

	if lc.pidFile != "" {
		if err := WritePidFile(lc.pidFile, os.Getpid()); err != nil {
			lc.abort()
			return err
		}
		lc.pidWritten = true
	}
	if lc.control != nil {
		stop, err := lc.control()
		if err != nil {
			// Not fatal: the mount still serves, but `kfuse status` and
			// `kfuse checkpoint` lose their fast path to this daemon.
			klog.Warn("control socket unavailable", klog.Err(err))
		} else {
			lc.stopCtl = stop
		}
	}
	lc.stopSig = lc.installSignalUnmount(bgCtx)

	if lc.onReady != nil {
		if err := lc.onReady(); err != nil {
			// The mount is live but whoever waits on it will never learn
			// that: take it down rather than leave an orphan the user
			// believes never started.
			lc.abort()
			return err
		}
	}
	lc.handle.Wait()
	lc.served.Store(true)
	return nil
}

// unmount asks the kernel to detach the live mount, at most once per run and
// never after serving has already stopped (an external unmount beat us).
func (lc *lifecycle) unmount() {
	lc.unmountOnce.Do(func() {
		if lc.served.Load() {
			return
		}
		if err := lc.handle.Unmount(); err != nil {
			// The mount is still live here: report it so the operator knows
			// the process will not exit on its own.
			klog.Error("unmount failed", klog.Err(err))
		}
	})
}

// abort takes down a mount that came up but whose run cannot continue, and
// waits for serving to stop so release may proceed.
func (lc *lifecycle) abort() {
	lc.unmount()
	lc.handle.Wait()
}

// release tears down everything run acquired, in the documented order. It is
// idempotent and only runs its body once; callers must ensure FUSE serving
// has stopped (handle.Wait returned) or the mount was never established.
func (lc *lifecycle) release() {
	lc.releaseOnce.Do(func() {
		if lc.stopSig != nil {
			lc.stopSig()
		}
		if lc.stopCtl != nil {
			lc.stopCtl()
		}
		if lc.bgCancel != nil {
			lc.bgCancel()
			lc.bg.Wait()
		}
		lc.sess.WaitCheckpoints()
		if lc.handle != nil {
			if err := lc.handle.Close(); err != nil {
				klog.Error("close mounter failed", klog.Err(err))
			}
		}
		if lc.pidWritten {
			if err := os.Remove(lc.pidFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				klog.Error("remove pidfile failed", "path", lc.pidFile, klog.Err(err))
			}
		}
		if lc.token != "" {
			timeout := lc.releaseTimeout
			if timeout == 0 {
				timeout = leaseReleaseTimeout
			}
			relCtx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := lc.leases.ReleaseLease(relCtx, lc.sess.ID(), lc.token); err != nil {
				klog.Error("release lease failed", "session", lc.sess.ID(), klog.Err(err))
			}
		}
		perf.Flush()
	})
}

func (lc *lifecycle) runLeaseHeartbeat(ctx context.Context) {
	every := lc.heartbeatEvery
	if every == 0 {
		every = registry.LeaseRefresh
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := lc.leases.RenewLease(ctx, lc.sess.ID(), lc.token, lc.host); err != nil {
				if ctx.Err() != nil {
					return
				}
				klog.Error("lease renew failed", "session", lc.sess.ID(), klog.Err(err))
				if errors.Is(err, registry.ErrLocked) {
					lc.sess.MarkLeaseLost()
				}
			}
		}
	}
}

func (lc *lifecycle) runMaterializer(ctx context.Context) {
	every := lc.materializeEvery
	if every == 0 {
		every = materializeInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := lc.sess.Checkpoint(ctx); err != nil && ctx.Err() == nil {
				klog.Error("checkpoint failed", "session", lc.sess.ID(), klog.Err(err))
			}
		}
	}
}

// installSignalUnmount unmounts on SIGINT/SIGTERM while the mount is live.
// The returned stop detaches the handler and waits for it to exit; after
// stop returns the handler will never touch the mount again.
func (lc *lifecycle) installSignalUnmount(ctx context.Context) (stop func()) {
	sig := make(chan os.Signal, 1)
	notify := lc.signals
	if notify == nil {
		notify = func(c chan<- os.Signal) func() {
			signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
			return func() { signal.Stop(c) }
		}
	}
	detach := notify(sig)
	quit := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-sig:
			lc.unmount()
		case <-quit:
		case <-ctx.Done():
		}
	}()
	return func() {
		detach()
		close(quit)
		<-exited
	}
}
