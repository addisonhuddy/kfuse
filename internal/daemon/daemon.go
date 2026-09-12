// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package daemon wires config + stores + session + FUSE into the kfuse
// mount lifecycle: resolve lower, open/create session, replay, serve, and
// unmount on signal. Also handles pidfiles and detach (re-exec).
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/addisonhuddy/kfuse/internal/blobstore"
	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/kafkalog"
	klog "github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
)

type Stack struct {
	Config config.Config
	Blobs  *blobstore.BlobStore
	Log    *kafkalog.Log
	Reg    *registry.Registry
}

// materializeInterval is how often the upper state is checkpointed to S3 while mounted.
const materializeInterval = 30 * time.Second

// leaseReleaseTimeout bounds lease cleanup during unmount.
const leaseReleaseTimeout = 15 * time.Second

func NewStack(ctx context.Context, cfg config.Config) (*Stack, error) {
	blobs, err := blobstore.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	log, err := kafkalog.New(cfg)
	if err != nil {
		return nil, err
	}
	// Topic creation is best-effort (pre-created topics are fine); the
	// partition count for session-key hashing must match the real topic.
	if err := log.EnsureTopic(ctx); err != nil {
		klog.Warn("ensure topic failed", klog.Err(err))
	}
	if err := log.RefreshPartitions(ctx); err != nil {
		_ = log.Close()
		return nil, err
	}
	reg, err := registry.New(ctx, cfg)
	if err != nil {
		_ = log.Close()
		return nil, err
	}
	return &Stack{Config: cfg, Blobs: blobs, Log: log, Reg: reg}, nil
}

// Close releases the stack's pooled broker connections.
func (s *Stack) Close() error {
	return s.Log.Close()
}

// ResolveLower returns the absolute lower path and its logical ID. The ID
// comes from the flag, KF_LOWER_ID, an existing /.kf-lower-id marker, or is
// minted and persisted (dev convenience; prod bakes KF_LOWER_ID into the
// image). Callers must agree on the ID across hosts.
func ResolveLower(lowerPath, lowerIDOverride string) (string, string, error) {
	abs, err := filepath.Abs(lowerPath)
	if err != nil {
		return "", "", err
	}
	id := lowerIDOverride
	if id == "" {
		id = os.Getenv("KF_LOWER_ID")
	}
	if id == "" {
		marker := filepath.Join(abs, ".kf-lower-id")
		b, err := os.ReadFile(marker)
		switch {
		case err == nil:
			if cand := strings.TrimSpace(string(b)); cand != "" {
				id = cand
			}
		case !errors.Is(err, os.ErrNotExist):
			// Minting a new id here would silently point the mount at an empty
			// session namespace and overwrite the existing marker.
			return "", "", fmt.Errorf("daemon: read lower id marker: %w", err)
		}
	}
	if id == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return "", "", err
		}
		id = "lower-" + hex.EncodeToString(b)
		marker := filepath.Join(abs, ".kf-lower-id")
		if err := os.WriteFile(marker, []byte(id), 0o644); err != nil {
			return "", "", fmt.Errorf("daemon: persist lower id: %w", err)
		}
	}
	return abs, id, nil
}

// New creates and registers a fresh session.
func (s *Stack) NewSession(ctx context.Context, lowerID string) (*session.Session, error) {
	return session.NewSession(ctx, lowerID, s.Blobs, s.Log, s.Reg)
}

// Open resumes an existing session by id.
func (s *Stack) OpenSession(ctx context.Context, id, lowerID string) (*session.Session, error) {
	return session.OpenSession(ctx, id, lowerID, s.Blobs, s.Log, s.Reg)
}

// BranchSession creates a child session branched from parentID at toOffset
// (pass toOffset < 0 for parent's current committed tail).
func (s *Stack) BranchSession(ctx context.Context, parentID string, toOffset int64, lowerID string) (*session.Session, error) {
	return session.Branch(ctx, parentID, toOffset, lowerID, s.Blobs, s.Log, s.Reg)
}

// CheckpointSession forces a state-image snapshot for the session and returns
// the durable covered Kafka offset.
func (s *Stack) CheckpointSession(ctx context.Context, sessionID, lowerID string) (int64, error) {
	sess, err := s.OpenSession(ctx, sessionID, lowerID)
	if err != nil {
		return -1, err
	}
	return sess.Checkpoint(ctx)
}

// ---------------------------------------------------------------------------
// State dir / pidfiles

func (s *Stack) stateDir() string { return s.Config.StateDir }

// ListSessions returns the session IDs recorded for a lower id.
func (s *Stack) ListSessions(ctx context.Context, lowerID string) ([]string, error) {
	return s.Reg.ListByLower(ctx, lowerID)
}

// SelectedPath returns the local-only "selected session" marker for a lower.
func SelectedPath(stateDir, lowerID string) string {
	return filepath.Join(stateDir, lowerID, "selected")
}

// ReadSelected returns the selected session id, or "" if none was recorded.
// An unreadable marker is an error: silently returning "" would mount a brand
// new session over the lower instead of the one the user selected.
func ReadSelected(stateDir, lowerID string) (string, error) {
	b, err := os.ReadFile(SelectedPath(stateDir, lowerID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("daemon: read selected session: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteSelected records the selected session id in the local state directory.
func WriteSelected(stateDir, lowerID, sessionID string) error {
	p := SelectedPath(stateDir, lowerID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(sessionID+"\n"), 0o644)
}

// PidFilePath returns the pidfile for a session.
func (s *Stack) PidFilePath(sess *session.Session) string {
	return filepath.Join(s.stateDir(), sess.LowerID(), sess.ID(), "pid")
}

func WritePidFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644)
}

func ReadPidFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// IsAlive reports whether a pid currently runs.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// ---------------------------------------------------------------------------
// Detach (daemonize): re-exec self with --foreground and a ready pipe.

// Detach launches a background copy of the mount command. The parent blocks
// until the child reports readiness and then returns the child pid.
func Detach(args []string, logPath string, readyPipe *os.File) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "KFUSE_DAEMON=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.ExtraFiles = []*os.File{readyPipe}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
