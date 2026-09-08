// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/addisonhuddy/kfuse/internal/perf"
)

var version = "dev"

// newRootCmd builds a complete, independent command tree: no flag state is
// shared between trees, so tests can construct several.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "kfuse",
		Short:         "branching overlay filesystem for coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.AddCommand(newSessionCmd(), newVersionCmd(), newMountCmd(), newUmountCmd(), newStatusCmd(), newCheckpointCmd())
	return root
}

func main() {
	err := newRootCmd().Execute()
	perf.Flush()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kfuse:", err)
		os.Exit(1)
	}
}
