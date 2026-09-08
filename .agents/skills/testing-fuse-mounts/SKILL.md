---
name: testing-fuse-mounts
description: How to runtime-test kfuse FUSE mount behaviour (fd lifecycle, overlay reads, shutdown) on a Devin box without Kafka/S3 credentials.
---

# Runtime-testing kfuse mounts

## Environment prerequisites
- `/dev/fuse` exists on the Devin box, but `fusermount`/`fusermount3` is usually **missing**, and
  `go-fuse` shells out to it (`internal/fs/fs.go` uses default `fuse.MountOptions`, no `DirectMount`).
  Install it first: `sudo apt-get install -y fuse3` (passwordless sudo works). Without this, every
  `Mounter.Mount()` fails with a fusermount lookup error and you may mistakenly conclude "FUSE is not permitted".
- Go 1.26 comes from the blueprint. Consider adding `sudo apt-get install -y fuse3` to the blueprint
  `initialize` step so mount tests work out of the box.

## What you can and cannot run without credentials
- `cmd/kfuse mount`, `cmd/fuseprobe`, `examples/*/run.sh` and the `integration`-tagged tests all go through
  `internal/config.FromEnv`, which **requires** `KF_KAFKA_BROKERS`, `KF_KAFKA_SASL_USERNAME`,
  `KF_KAFKA_SASL_PASSWORD`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `KF_BLOB_BUCKET`.
  There is **no S3 endpoint override in production code** (only the test-only `internal/s3fake`), so a
  fully local Kafka+MinIO stack cannot be substituted — plan around it or request the secrets.
- You *can* mount for real with no creds by building a tiny throwaway `cmd/<harness>` that uses the real
  `internal/fs.Mounter`:
  `upper.New()` + `Apply` a few `kfusev1.EventEnvelope`s (SessionStart / Create / Unlink) →
  `session.NewWithState(registry.Session{...}, u, nil, nil, nil)` → `&fs.Mounter{LowerPath: dir, Session: sess}`.
  With `Blobs: nil` you get lower passthrough, readdir merge and whiteouts; overlay *byte* reads need S3, so
  assert on overlay presence in readdir rather than its contents.
  Delete the harness afterwards (`rm -rf cmd/<harness>`) — it otherwise shows up in `go test ./...` output.

## Useful patterns
- **fd accounting**: read `len(os.ReadDir("/proc/self/fd"))` before the first mount and after each
  mount/unmount cycle; run ≥5 cycles and assert zero growth. From outside, `ls -l /proc/<pid>/fd` shows the
  lower dirfd as a symlink to the lower directory path while the mount is live.
- **Negative control**: `git worktree add /tmp/<name> origin/main`, copy the harness in with the fix's call
  removed, and confirm it *does* leak (+1 fd/cycle). Without this, a flat fd count proves little.
- **Daemon lifecycle**: replicate `internal/daemon/daemon.go` Mount(): mount → `defer m.Close()` →
  SIGINT/SIGTERM handler calling `server.Unmount()` → `server.Wait()`. Verify afterwards with
  `findmnt <lower>` (exit 1 = unmounted) and `ls <lower>` (lower-only contents = no stale mount).
- Get the harness PID from its own stdout (`os.Getpid()`); `pgrep -f` often matches the launching wrapper.
- If a run leaves `Transport endpoint is not connected`, clean up with
  `fusermount3 -u <lower>` (or `sudo umount -l <lower>`) before retrying.
- For a watchable recording of this CLI/daemon work, use `konsole` (KDE is installed; xterm/gnome-terminal are
  not), maximize with `wmctrl -r :ACTIVE: -b add,maximized_vert,maximized_horz`, and zoom the font with
  `ctrl+plus` keypresses (`ctrl+shift+plus` types literal `+` characters).

## Devin Secrets Needed
For `kfuse mount`, `cmd/fuseprobe` and the integration tests: `KF_KAFKA_BROKERS`,
`KF_KAFKA_SASL_USERNAME`, `KF_KAFKA_SASL_PASSWORD` (Confluent **cluster-level** Kafka API key, not an
org/global key — SASL fails with `[58]`), `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
`KF_BLOB_BUCKET`.
