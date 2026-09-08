// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package daemon

// maxUnixSocketPath is the size of sockaddr_un.sun_path on Linux and the other
// platforms we build for, including the terminating NUL.
const maxUnixSocketPath = 108
