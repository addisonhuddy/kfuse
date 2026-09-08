// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package daemon

// maxUnixSocketPath is the size of sockaddr_un.sun_path on Darwin, including
// the terminating NUL, so a usable path is at most maxUnixSocketPath-1 bytes.
const maxUnixSocketPath = 104
