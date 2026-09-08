// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/addisonhuddy/kfuse/internal/daemon"
	"github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/session"
)

const (
	// mountReadyTimeout bounds waiting for a daemonized mount to become ready.
	mountReadyTimeout = 60 * time.Second
	// daemonLogPollInterval controls retries while collecting startup logs.
	daemonLogPollInterval = 100 * time.Millisecond
)

type mountFlags struct {
	shared     sharedFlags
	foreground bool
}

func newMountCmd() *cobra.Command {
	flags := &mountFlags{}
	mount := &cobra.Command{
		Use:   "mount [session_id]",
		Short: "mount a session over the lower directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd.Context(), flags, args, cmd.OutOrStdout())
		},
	}
	flags.shared.add(mount)
	mount.Flags().BoolVar(&flags.foreground, "foreground", false, "stay in the foreground")
	return mount
}

func runMount(ctx context.Context, flags *mountFlags, args []string, out io.Writer) error {
	l, stack, err := flags.shared.resolveStack(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = stack.Close() }()
	cfg, lowerPath, lowerID := l.cfg, l.path, l.id

	var sessID string
	if len(args) == 1 {
		sessID = args[0]
	} else {
		selected, err := daemon.ReadSelected(cfg.StateDir, lowerID)
		if err != nil {
			return err
		}
		sessID = selected
	}

	if os.Getenv("KFUSE_DAEMON") == "1" {
		// Backgrounded child: serve and write readiness on fd 3.
		ready := os.NewFile(3, "ready")
		if ready != nil {
			defer func() { _ = ready.Close() }()
		}
		if err := serve(ctx, stack, sessID, lowerID, lowerPath, ready); err != nil {
			return err
		}
		return nil
	}

	if flags.foreground {
		return serve(ctx, stack, sessID, lowerID, lowerPath, os.Stdout)
	}

	// Daemonize: re-exec ourselves; the child writes the session id on the
	// ready pipe once its mount is live, then the parent prints it.
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = pr.Close() }()
	childArgs := []string{"mount"}
	if sessID != "" {
		childArgs = append(childArgs, sessID)
	}
	childArgs = append(childArgs,
		"--foreground",
		"--lower", flags.shared.lowerPath,
		"--lower-id", lowerID,
	)
	logPath := filepath.Join(cfg.StateDir, lowerID, "daemon.log")
	logStart := fileSize(logPath)
	pid, err := daemon.Detach(childArgs, logPath, pw)
	_ = pw.Close()
	if err != nil {
		return err
	}
	if err := pr.SetReadDeadline(time.Now().Add(mountReadyTimeout)); err != nil {
		return fmt.Errorf("mount: set ready deadline: %w", err)
	}
	buf := make([]byte, 256)
	n, err := pr.Read(buf)
	if err != nil {
		tail := tailFile(logPath, logStart, 10)
		for i := 0; tail == "" && i < 20; i++ {
			time.Sleep(daemonLogPollInterval)
			tail = tailFile(logPath, logStart, 10)
		}
		if tail != "" {
			return fmt.Errorf("mount: child failed to start (pid %d): %w\n%s", pid, err, tail)
		}
		return fmt.Errorf("mount: child failed to start (pid %d): %w", pid, err)
	}
	_, err = out.Write(buf[:n])
	return err
}

func serve(ctx context.Context, stack *daemon.Stack, sessID, lowerID, lowerPath string, ready *os.File) error {
	var (
		sess *session.Session
		err  error
	)
	if sessID == "" {
		sess, err = stack.NewSession(ctx, lowerID)
	} else {
		sess, err = stack.OpenSession(ctx, sessID, lowerID)
	}
	if err != nil {
		return err
	}
	// Losing the readiness write means the parent times out and reports
	// failure while this process keeps serving; returning the error makes
	// Serve unmount instead of leaving an orphaned daemon the user believes
	// never started.
	onReady := func() error {
		if ready == nil {
			return nil
		}
		if _, err := ready.WriteString(sess.ID() + "\n"); err != nil {
			err = fmt.Errorf("mount: signal readiness: %w", err)
			log.Error("mount not ready", log.Err(err))
			return err
		}
		return nil
	}
	return stack.Serve(ctx, sess, lowerPath, daemon.ServeOptions{
		PidFile:       stack.PidFilePath(sess),
		ControlSocket: true,
		OnReady:       onReady,
	})
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// tailFile returns the last n non-empty lines written to path after byte offset start.
func tailFile(path string, start int64, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) <= start {
		return ""
	}
	lines := strings.Split(string(data[start:]), "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	if len(nonEmpty) > n {
		nonEmpty = nonEmpty[len(nonEmpty)-n:]
	}
	return strings.Join(nonEmpty, "\n")
}
