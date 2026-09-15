# kfuse

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml/badge.svg)](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml)

kfuse is a branching overlay filesystem designed for agents, backed by Kafka
and S3. Mount a session over a base directory; mutations commit to Kafka, file
bytes land in S3, and the workspace can pause, resume, and branch across hosts.

The kfuse CLI interface was inspired by [Modal's Overeasy](https://github.com/modal-labs/overeasy).

## Status: public alpha

kfuse `v0.1.x` is an experimental public alpha — intended for evaluation,
demos, and development of agent workflows, not production workloads. There is
no cross-version compatibility guarantee for stored state during the alpha;
see the [CHANGELOG](CHANGELOG.md) and the
[compatibility policy](OPERATIONS.md#compatibility-policy) in OPERATIONS.md,
which also documents the security and concurrency boundaries.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh | bash
```

Downloads the matching prebuilt archive (Linux amd64/arm64, macOS
Intel/Apple Silicon), verifies its sha256 against `checksums.txt`, and
installs to `/usr/local/bin` when writable else `~/.local/bin`. Pin a version
or destination:

```sh
curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh | bash -s -- --version v0.1.0 --dest ~/bin
```

Requirements: Linux needs `fuse3` (`fusermount3` + `/dev/fuse`); macOS needs
[macFUSE](https://macfuse.github.io/) (`brew install --cask macfuse`).

Or grab the tarball for your platform from
[Releases](https://github.com/addisonhuddy/kfuse/releases) — archives include
`LICENSE`, `NOTICE`, and `THIRD_PARTY_LICENSES.md`.

### Docker

```sh
docker run --rm --privileged --device /dev/fuse \
  addisonhuddy/kfuse:0.1.0 --help
```

Tags: `0.1.0`, `v0.1`, `latest`. The image ships the binary only; mounting
still needs Kafka and S3 (or the local stack below).

### Build from source

```sh
git clone https://github.com/addisonhuddy/kfuse && cd kfuse && make build
```

Needs the Go version in `go.mod`. `./install.sh --source` installs a source
build; `GOOS=… GOARCH=… ./install.sh --source --output dist/kfuse`
cross-compiles to a file.

## Quick start

The quickest credential-free path is the local demo: Apache Kafka in
single-node KRaft mode plus MinIO, a demo container, and a real FUSE
walkthrough covering file operations, persistence, resume, checkpointing, and
branching. From the repo root on a Linux Docker host with `/dev/fuse`:

```sh
make local-demo      # scripted walkthrough; ends with COMPLETE (42/42)
./examples/local/demo.sh --shell   # or an interactive mount
make local-down      # stop Kafka and MinIO when finished
```

Run the stack only on a trusted development host — dummy credentials,
unauthenticated Kafka, and a privileged demo container. See the
[local demo guide](examples/local/README.md) and the
[E2B cloud-sandbox demo](examples/e2b-sandbox/README.md) (a workspace that
outlives its sandbox and branches into new ones).

## CLI

A **lower** is the read-only base directory; a **session** is a mutable view
over it. Configure storage via exported environment variables (`.env.example`
lists the names; the binary never reads `.env` itself — the example scripts
load it for you).

```sh
make local-up                                      # local Kafka + MinIO
set -a; . examples/local/local.env; set +a         # or export your own

BASE=$(mktemp -d /tmp/kfuse-lower.XXXXXX)
LOWER_ID=$(basename "$BASE")
SID=$(kfuse session new --lower "$BASE" --lower-id "$LOWER_ID")

kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
printf 'hello\n' > "$BASE/hello.txt"
kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"

kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
cat "$BASE/hello.txt"          # still 'hello' — the write survived remounting
kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"
```

To resume on another host, supply an equivalent base tree with the same lower
ID — the session ID alone does not capture the base contents, and a lower ID
must never be shared between unrelated base trees.

## How it works

The lower tree stays read-only; the session's in-memory upper supplies the
mutable overlay. File bytes are uploaded to content-addressed S3 objects
before each mutation event is appended to Kafka — the `acks=all` append is
the commit point. Checkpoints write a serialized state image to S3 so resume
replays only newer records, and a branch reconstructs the parent's state at a
chosen offset. One writer per session is coordinated by a best-effort S3
lease. Details: [OPERATIONS.md](OPERATIONS.md).

## Limits and troubleshooting

- Mounts need FUSE: Linux `fuse3`, or macOS with macFUSE. Windows is not
  supported; use the E2B demo or a Linux VM. Docker Desktop mounts are not
  validated.
- Supported: regular files, directories, symlinks, rename, truncate,
  attributes, fsync. Not supported: hardlinks, xattrs, ACLs, advisory locks,
  device/socket/FIFO files.
- Kafka retention and S3 lifecycle bound how far back resume/branch can
  reach; nothing expires remote state automatically. See
  [OPERATIONS.md](OPERATIONS.md) for the settings that keep history
  replayable and the [troubleshooting table](OPERATIONS.md#troubleshooting).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and contribution
instructions. Security reports go through [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
