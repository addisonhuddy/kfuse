// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package fs

// FUSE passes Linux rename(2) flag values regardless of host OS.
const (
	renameNoReplace = 1 << 0
	renameExchange  = 1 << 1
)
