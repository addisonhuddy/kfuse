// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package fs

import "golang.org/x/sys/unix"

const (
	renameNoReplace = unix.RENAME_NOREPLACE
	renameExchange  = unix.RENAME_EXCHANGE
)
