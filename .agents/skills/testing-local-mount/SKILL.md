---
name: testing-local-mount
description: Stand up a credential-free local stack (Apache Kafka KRaft + MinIO) so an unmodified kfuse binary can do a real FUSE mount, and hammer it for concurrency bugs.
---

# Testing kfuse with a real mount, without cloud credentials

Hosted Confluent Cloud + AWS S3 remains the preferred way to run kfuse, but the
binary is provider-neutral: set `BOOTSTRAP_SERVER`, `KAFKA_TLS`, and the
`S3_*` variables to local services instead. `examples/local` owns the local
stack: Apache Kafka in single-node KRaft mode and MinIO behind a direct
`S3_ENDPOINT`.

```sh
make local-up        # docker compose: Kafka on 127.0.0.1:9092, MinIO on :9000
set -a; . examples/local/local.env; set +a
```

The local Kafka listener is unauthenticated, so `KAFKA_SASL_USERNAME` and
`KAFKA_SASL_PASSWORD` are intentionally absent. MinIO still requires SigV4
credentials; `local.env` contains only dummy local values. `S3_PATH_STYLE=true`
avoids bucket-subdomain DNS, so no `/etc/hosts`, TLS certificate, or `sudo`
setup is needed.

Both images are pulled from Docker Hub, so on a network-restricted box the
allowlist must include `registry-1.docker.io`, `auth.docker.io` and
`production.cloudflare.docker.com` (otherwise `docker pull` dies with
`Get "https://registry-1.docker.io/v2/": EOF`); request it before planning a
mount run.

## Quick checks

`kfuse session new` creates `kfuse.events` with the configured partition count
when it is absent. To verify MinIO contents, use the `createbucket` service's
credentials through `mc`, or any S3 client pointed at `S3_ENDPOINT` with
path-style addressing.

## Mounting

FUSE works directly on a Linux box with `/dev/fuse` and `fusermount3`:

```sh
mkdir -p /tmp/kf-harness/lower /tmp/kf-harness/state
export KF_STATE_DIR=/tmp/kf-harness/state KF_LOWER_ID=local-lower
SID=$(kfuse session new --lower /tmp/kf-harness/lower --lower-id local-lower)
kfuse mount "$SID" --foreground --lower /tmp/kf-harness/lower --lower-id local-lower \
  >mount.log 2>&1 &
# wait for: grep " /tmp/kf-harness/lower " /proc/mounts
kill -TERM $!   # clean unmount; fusermount3 -u <dir> to force
```

`examples/local/walkthrough.sh` is the canonical golden path — mirror its
assertions instead of inventing new ones. On Linux Docker hosts,
`./examples/local/demo.sh` runs that demo in a privileged container using
`examples/local/container.env` (joins the private `kfuse-local` Docker network).

Shut down with `make local-down`; add `--volumes` to `examples/local/down.sh`
when deleting the Kafka and MinIO named volumes is intended.

## Concurrency / overlay-state testing

* Build the daemon with the race detector: `go build -race -o kfuse-race ./cmd/kfuse`.
  Race reports land in the mount log (`grep -c 'DATA RACE' mount.log`).
* Always A/B against the merge base to prove detection power:
  `git worktree add /tmp/kf-base <base-sha>` → build a second `-race` binary
  (`git worktree remove --force` afterwards). A storm that is clean on both
  binaries proves nothing.
* A strong data-corruption invariant for the overlay: put a 1 MiB lower file of
  a single byte value (`L`), sparse-CoW 4 KiB blocks of `X` into it from several
  processes while readers assert the file is *always* exactly 1048576 bytes and
  contains *only* `L`/`X`. A NUL byte or short read means a stale extent list
  was mixed with a fresh size. Make writes idempotent/non-overlapping so the
  final md5 is deterministic and can be compared exactly, then unmount/resume
  and compare the md5 again.

## Known pre-existing behaviour (do not blame your change)

`chmod 640` on a **lower-backed** file used to read back as `640` while mounted
but lose the override after unmount + resume (reads `644`), with or without a
`kfuse checkpoint` in between (reproduced at 270426e). This is fixed by PR #38
(statAttr merges upper.AttrOverride); on branches containing that fix, expect
overrides (mode and mtime) to survive resume.

Resuming a session takes noticeably longer than the first mount (state-image
fetch); after backgrounding `kfuse mount`, poll `/proc/mounts` in a loop
(`until grep -q " <lower> " /proc/mounts; do sleep 1; done`) instead of a fixed
`sleep 3`, or you will stat the raw lower dir and get confusing results.

renameat2 flags (RENAME_NOREPLACE/RENAME_EXCHANGE) can't be exercised from
plain `mv` or Python `os.rename`; use a small ctypes helper calling
`libc.renameat2(AT_FDCWD, src, AT_FDCWD, dst, flags)` (NOREPLACE=1, EXCHANGE=2).

## Devin Secrets Needed

None for this local harness. A real hosted run needs a repo-root `.env` with
`BOOTSTRAP_SERVER`, `KAFKA_SASL_USERNAME`, `KAFKA_SASL_PASSWORD`, `S3_REGION`,
`S3_ACCESS_KEY`, `S3_SECRET_KEY`, and `S3_BUCKET` (Confluent Cloud *Kafka* API
key, not an org key, when using Confluent Cloud).
