// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/session"
)

// maxControlLine caps a control request so a local client cannot make the
// daemon buffer an unbounded line.
const (
	maxControlLine = 4 << 10

	// controlConnTimeout bounds a control handler request.
	controlConnTimeout = 30 * time.Second
	// controlDialTimeout bounds connecting to a control socket.
	controlDialTimeout = 3 * time.Second
	// controlRequestTimeout bounds waiting for a control reply.
	controlRequestTimeout = 10 * time.Second
)

// ControlSocketPath returns the unix socket path a mounted session's daemon
// serves control commands on. The session state directory is preferred so the
// socket lives beside the rest of the session state, but a deep state dir can
// push the path past the platform's sockaddr_un limit (notably on macOS, where
// the default temp dir alone is ~50 bytes); in that case the socket goes in a
// per-user temp directory under a name derived from the session identity, so
// every process computes the same path.
func ControlSocketPath(stateDir, lowerID, sessID string) string {
	path := filepath.Join(stateDir, lowerID, sessID, "control.sock")
	if len(path) < maxUnixSocketPath {
		return path
	}
	sum := sha256.Sum256([]byte(stateDir + "\x00" + lowerID + "\x00" + sessID))
	name := hex.EncodeToString(sum[:8]) + ".sock"
	dir := fmt.Sprintf("kfuse-%d", os.Getuid())
	for _, base := range []string{os.TempDir(), "/tmp"} {
		if base == "" {
			continue
		}
		if short := filepath.Join(base, dir, name); len(short) < maxUnixSocketPath {
			return short
		}
	}
	return filepath.Join("/tmp", name)
}

// checkPrivateDir fails unless dir is owned by the current user and closed to
// everyone else.
func checkPrivateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("daemon: cannot verify ownership of %s", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("daemon: control socket dir %s is owned by uid %d", dir, st.Uid)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon: control socket dir %s is accessible to other users (mode %o)", dir, fi.Mode().Perm())
	}
	return nil
}

// StartControlServer opens the control socket and serves one-line commands
// ("checkpoint", "status") against the live session for its lifetime. It
// returns a stop function that closes the listener, waits for in-flight
// handlers to finish, and removes the socket.
func (s *Stack) StartControlServer(sess *session.Session) (stop func(), err error) {
	path := ControlSocketPath(s.Config.StateDir, sess.LowerID(), sess.ID())
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, filepath.Clean(s.Config.StateDir)+string(os.PathSeparator)) {
		// The fallback path lives in a shared temp dir, so refuse to serve
		// checkpoint commands from a directory another user could have
		// pre-created and could swap the socket in.
		if err := checkPrivateDir(dir); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("daemon: clear stale control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// The socket drives checkpoints on the mount, so restrict it to the owner
	// instead of whatever the process umask happens to be.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	done := make(chan struct{})
	var handlers sync.WaitGroup
	go func() {
		defer close(done)
		defer func() { _ = ln.Close() }()
		defer func() { _ = os.Remove(path) }()
		for {
			conn, err := ln.Accept()
			if err != nil {
				// Closing the listener is the normal shutdown path; anything
				// else silently ends control-socket service.
				if !errors.Is(err, net.ErrClosed) {
					log.Error("control socket accept failed", log.Err(err))
				}
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				s.handleControl(conn, sess)
			}()
		}
	}()
	return func() {
		_ = ln.Close()
		<-done
		// Handlers hold a live *session.Session; returning before they finish
		// would let a slow checkpoint touch a destroyed session.
		handlers.Wait()
	}, nil
}

func (s *Stack) handleControl(conn net.Conn, sess *session.Session) {
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(controlConnTimeout)); err != nil {
		log.Error("control connection deadline failed", log.Err(err))
		return
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxControlLine)).ReadString('\n')
	if err != nil {
		log.Error("control read failed", log.Err(err))
		return
	}
	command := strings.TrimSpace(line)
	var replyErr error
	switch command {
	case "checkpoint":
		off, err := sess.Checkpoint(context.Background())
		if err != nil {
			// The client only sees the reply line, so record the cause here too.
			log.Error("control checkpoint failed", "session", sess.ID(), log.Err(err))
			_, replyErr = fmt.Fprintf(conn, "error %v\n", err)
			break
		}
		_, replyErr = fmt.Fprintf(conn, "offset %d\n", off)
	case "status":
		_, replyErr = fmt.Fprintf(conn, "status %s %d\n", sess.ID(), sess.Upper().SeqLast())
	default:
		_, replyErr = fmt.Fprintf(conn, "error unknown command %q\n", command)
	}
	if replyErr != nil {
		log.Error("control reply failed", "command", command, log.Err(replyErr))
	}
}

// ControlRequest sends a one-line command to a control socket and returns the
// reply line.
func ControlRequest(path, command string) (string, error) {
	conn, err := net.DialTimeout("unix", path, controlDialTimeout)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(controlRequestTimeout)); err != nil {
		return "", fmt.Errorf("daemon: control deadline: %w", err)
	}
	if _, err := conn.Write([]byte(command + "\n")); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(reply), nil
}
