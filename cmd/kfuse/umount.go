// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/daemon"
	"github.com/addisonhuddy/kfuse/internal/log"
)

const (
	// daemonShutdownTimeout bounds waiting for the serving daemon to exit.
	daemonShutdownTimeout = 30 * time.Second
	// daemonPollInterval controls how often daemon liveness is checked.
	daemonPollInterval = 200 * time.Millisecond
)

func newUmountCmd() *cobra.Command {
	flags := &sharedFlags{}
	umount := &cobra.Command{
		Use:   "umount",
		Short: "unmount the mounted session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := flags.resolveLocal()
			if err != nil {
				return err
			}
			// Find the serving daemon: the session for this lower is the one
			// whose pidfile is present (mount with no arg = most recent).
			pid, err := currentDaemonPid(l.cfg, l.id)
			if err != nil {
				return err
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("umount: signal %d: %w", pid, err)
			}
			deadline := time.Now().Add(daemonShutdownTimeout)
			for daemon.IsAlive(pid) && time.Now().Before(deadline) {
				time.Sleep(daemonPollInterval)
			}
			if daemon.IsAlive(pid) {
				return fmt.Errorf("umount: daemon pid %d still running after 30s", pid)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "unmounted")
			return err
		},
	}
	flags.add(umount)
	return umount
}

func newStatusCmd() *cobra.Command {
	flags := &sharedFlags{}
	status := &cobra.Command{
		Use:   "status",
		Short: "exit 0 when a mount is live; print session + offsets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := flags.resolveLocal()
			if err != nil {
				return err
			}
			sessID, pid, err := currentDaemonSession(l.cfg, l.id)
			if err != nil {
				return err
			}
			if !daemon.IsAlive(pid) {
				return fmt.Errorf("status: daemon pid %d not running", pid)
			}
			// The mount is already known live from the pidfile, so a failed or
			// unparsable status reply only costs the sequence number - but it is
			// reported so "seq ?" is not mistaken for an idle session.
			seq := "?"
			reply, err := daemon.ControlRequest(daemon.ControlSocketPath(l.cfg.StateDir, l.id, sessID), "status")
			switch {
			case err != nil:
				log.Warn("status request failed", log.Err(err))
			default:
				if f := strings.Fields(reply); len(f) >= 3 && f[0] == "status" {
					seq = f[2]
				} else {
					log.Warn("unexpected status reply", "reply", strings.TrimSpace(reply))
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "mounted: session %s pid %d seq %s\n", sessID, pid, seq)
			return err
		},
	}
	flags.add(status)
	return status
}

func newCheckpointCmd() *cobra.Command {
	flags := &sharedFlags{}
	cmd := &cobra.Command{
		Use:   "checkpoint [session_id]",
		Short: "force a state-image flush to S3 and print the covered Kafka offset",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The live daemon flushes its own upper over the control socket,
			// which needs no cloud access; only the log-based fallback does.
			l, err := flags.resolveLocal()
			if err != nil {
				return err
			}
			var sessID string
			if len(args) == 1 {
				sessID = args[0]
			} else {
				var err error
				sessID, _, err = currentDaemonSession(l.cfg, l.id)
				if err != nil {
					return fmt.Errorf("checkpoint: %w (pass session_id explicitly)", err)
				}
				// Talk to the live daemon first: it flushes its in-memory
				// upper directly instead of reconstructing from the log.
				// Falling back to a log-based checkpoint is correct but slower and
				// cannot see unflushed daemon state, so say why we fell back.
				reply, rerr := daemon.ControlRequest(daemon.ControlSocketPath(l.cfg.StateDir, l.id, sessID), "checkpoint")
				switch {
				case rerr != nil:
					log.Warn("daemon checkpoint request failed; rebuilding from the log", log.Err(rerr))
				case strings.HasPrefix(reply, "offset "):
					_, err = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimPrefix(reply, "offset "))
					return err
				default:
					log.Warn("daemon checkpoint reply unexpected; rebuilding from the log", "reply", strings.TrimSpace(reply))
				}
			}
			if err := l.cfg.Validate(); err != nil {
				return err
			}
			stack, err := daemon.NewStack(cmd.Context(), l.cfg)
			if err != nil {
				return err
			}
			defer func() { _ = stack.Close() }()
			offset, err := stack.CheckpointSession(cmd.Context(), sessID, l.id)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), offset)
			return err
		},
	}
	flags.add(cmd)
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}

// currentDaemonSession finds the active session id and pid for lowerID.
func currentDaemonSession(cfg config.Config, lowerID string) (string, int, error) {
	dir := filepath.Join(cfg.StateDir, lowerID)
	sessions, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, fmt.Errorf("no mount for lower %q (state: %s)", lowerID, dir)
		}
		return "", 0, fmt.Errorf("read state dir %s: %w", dir, err)
	}
	for _, e := range sessions {
		if !e.IsDir() {
			continue
		}
		pid, err := daemon.ReadPidFile(filepath.Join(dir, e.Name(), "pid"))
		if err != nil {
			continue
		}
		if daemon.IsAlive(pid) {
			return e.Name(), pid, nil
		}
	}
	return "", 0, fmt.Errorf("no live mount for lower %q", lowerID)
}

// currentDaemonPid finds the serving daemon for this lower: any session
// directory with a pidfile whose process is alive.
func currentDaemonPid(cfg config.Config, lowerID string) (int, error) {
	_, pid, err := currentDaemonSession(cfg, lowerID)
	return pid, err
}
