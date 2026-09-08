// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/daemon"
	"github.com/addisonhuddy/kfuse/internal/registry"
)

type sharedFlags struct {
	lowerPath string
	lowerID   string
}

func (f *sharedFlags) add(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVarP(&f.lowerPath, "lower", "l", ".", "lower directory to mount over (default cwd)")
	cmd.PersistentFlags().StringVar(&f.lowerID, "lower-id", "", "logical lower ID (default KF_LOWER_ID or minted + persisted)")
}

// lower is the env config plus the resolved lower every command starts from.
// cfg is only guaranteed valid for the dependencies the resolving method
// promised: resolveLocal checks nothing beyond the local fields.
type lower struct {
	cfg  config.Config
	path string
	id   string
}

// resolveLocal loads the environment and resolves the lower directory and its
// logical ID without requiring any Kafka or S3 variable: local commands
// (select, status, umount, the daemon control path) must work without cloud
// credentials.
func (f *sharedFlags) resolveLocal() (lower, error) {
	cfg, err := config.Load()
	if err != nil {
		return lower{}, err
	}
	lowerPath, lowerID, err := daemon.ResolveLower(f.lowerPath, f.lowerID)
	if err != nil {
		return lower{}, err
	}
	return lower{cfg: cfg, path: lowerPath, id: lowerID}, nil
}

// resolve is resolveLocal plus full Kafka + S3 validation, for commands that
// will dial the stack.
func (f *sharedFlags) resolve() (lower, error) {
	l, err := f.resolveLocal()
	if err != nil {
		return lower{}, err
	}
	if err := l.cfg.Validate(); err != nil {
		return lower{}, err
	}
	return l, nil
}

// resolveStack additionally dials the Kafka/S3 stack.
func (f *sharedFlags) resolveStack(ctx context.Context) (lower, *daemon.Stack, error) {
	l, err := f.resolve()
	if err != nil {
		return lower{}, nil, err
	}
	stack, err := daemon.NewStack(ctx, l.cfg)
	if err != nil {
		return lower{}, nil, err
	}
	return l, stack, nil
}

// resolveRegistry validates and opens only the S3 registry: no broker is
// dialed, no topic is created.
func (f *sharedFlags) resolveRegistry(ctx context.Context) (lower, *registry.Registry, error) {
	l, err := f.resolveLocal()
	if err != nil {
		return lower{}, nil, err
	}
	if err := l.cfg.ValidateStorage(); err != nil {
		return lower{}, nil, err
	}
	reg, err := registry.New(ctx, l.cfg)
	if err != nil {
		return lower{}, nil, err
	}
	return l, reg, nil
}

func newSessionCmd() *cobra.Command {
	flags := &sharedFlags{}
	session := &cobra.Command{
		Use:   "session",
		Short: "session management",
	}
	flags.add(session)
	newCmd := &cobra.Command{
		Use:   "new",
		Short: "create a new session and print its id",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, stack, err := flags.resolveStack(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = stack.Close() }()
			sess, err := stack.NewSession(cmd.Context(), l.id)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), sess.ID())
			return err
		},
	}
	var toOffset int64
	branchCmd := &cobra.Command{
		Use:   "branch <parent_session_id>",
		Short: "create a child session from a parent offset and print its id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parentID := args[0]
			l, stack, err := flags.resolveStack(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = stack.Close() }()
			child, err := stack.BranchSession(cmd.Context(), parentID, toOffset, l.id)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), child.ID())
			return err
		},
	}
	branchCmd.Flags().Int64Var(&toOffset, "to", -1, "parent offset to branch from (-1 = current committed tail)")

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "list sessions for this lower",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, reg, err := flags.resolveRegistry(cmd.Context())
			if err != nil {
				return err
			}
			ids, err := reg.ListByLower(cmd.Context(), l.id)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), id); err != nil {
					return err
				}
			}
			return nil
		},
	}

	selectCmd := &cobra.Command{
		Use:   "select <session_id>",
		Short: "set the default session for `kfuse mount` (local only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := flags.resolveLocal()
			if err != nil {
				return err
			}
			if err := daemon.WriteSelected(l.cfg.StateDir, l.id, args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), args[0])
			return err
		},
	}

	session.AddCommand(newCmd, branchCmd, lsCmd, selectCmd)
	return session
}
