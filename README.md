# kfuse

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml/badge.svg)](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml)

kfuse is a branching overlay filesystem designed for agents, backed by Kafka
and S3. Mount a session over a base directory; mutations commit to Kafka, file
bytes land in S3, and the workspace can pause, resume, and branch across hosts.

- **Persist**: every write is a Kafka record; the workspace survives the
  process, container, or sandbox that made it.
- **Resume**: mount the same session on any Linux host and pick up where you
  left off.
- **Branch**: fork a session at any committed offset into independent
  children — try N approaches from one starting point.

```text
              agent / shell / tool
                       |
            +----------v-----------+
            |     kfuse mount      |  FUSE: lower (read-only base tree)
            |   lower  +  upper    |        + in-memory upper (mutations)
            +-----+----------+-----+
                  |          |
         mutation events   file bytes
                  |          |
           +------v-----+ +--v-----------------+
           |   Kafka    | |        S3          |
           |   topic    | | content-addressed  |
           |  (commit   | | blobs, state       |
           |   point)   | | images, lease      |
           +------------+ +--------------------+

     checkpoint = state image in S3 covering Kafka offset N
     branch     = new session that replays the parent up to offset N
```

**Try it in one command** (Linux Docker host with `/dev/fuse`, no credentials):

```sh
git clone https://github.com/addisonhuddy/kfuse.git && cd kfuse && make local-demo
```

That starts Kafka + MinIO, mounts a session, writes files, unmounts, resumes,
checkpoints, and branches — ending with `COMPLETE (42/42)`. Then read
[Getting Started](#getting-started) or jump to the [CLI reference](#cli-reference).

The kfuse CLI interface was inspired by [Modal's Overeasy](https://github.com/modal-labs/overeasy).

## Status: public alpha

kfuse `v0.1.x` is an experimental public alpha. It is intended for evaluation,
demos, and development of agent workflows — not for production workloads.
Expect rough edges: there are no stability, support, or production-readiness
guarantees, and behavior, CLI surfaces, and stored formats may change between
releases.

### Security and concurrency boundaries

- **Persistence and branching, not a security sandbox.** kfuse does not
  isolate untrusted code. The demos run privileged containers and disposable
  cloud sandboxes for convenience; neither is a hostile-code isolation
  boundary. Code running inside a kfuse mount — including agent code in the
  examples — needs its own sandboxing, and the Kafka/S3 credentials you give
  kfuse should be scoped to what a compromised mount could reach.
- **The writer lease is best-effort, not an atomic distributed lock.** As
  described in [How it works](#how-it-works), mount refuses a second healthy
  writer while a live lease exists, but concurrent writers are not strongly
  fenced: a frozen or crashed writer can miss losing the lease until its next
  renewal, so split-brain around lease expiry is a real operational
  limitation, not a correctness guarantee.
- **Not the sole copy of your data.** kfuse can only resume or branch as far
  back as retained Kafka records and S3 state images allow (see
  [Limits](#limits)); Kafka retention and bucket lifecycle policy bound
  recovery, and remote state cleanup is not automatic. Keep independent
  backups of important data and verify your retention settings before relying
  on resume or branch for anything you cannot afford to lose.

### Compatibility policy

During the alpha period, kfuse does not guarantee cross-version compatibility
for stored state. Sessions, checkpoint state images, and Kafka event records
written by
one release may not be readable by a different release. Resume and branch
within the release that created the session, and treat existing remote state
as disposable across upgrades. Breaking changes to stored formats will be
called out in the release notes; this policy will be revisited before a stable
release.

## Getting Started

Pick how you want to run the CLI, then try one of the demos below.

| Method | Best for |
|---|---|
| [Binary](#binary) | Linux host with direct CLI use |
| [Docker](#docker) | Quick isolated run on a Linux container host |
| [Source](#source) | Development or building for a different architecture |

The mount runtime still needs Linux, `/dev/fuse`, and `fusermount3`. macOS and
Windows cannot host a kfuse mount natively; use the E2B demo or a Linux VM.
Docker Desktop mount support is not currently validated.

### Binary

Download the [latest release](https://github.com/addisonhuddy/kfuse/releases/latest)
for your architecture and extract it:

```sh
VER=$(curl -fsSL https://api.github.com/repos/addisonhuddy/kfuse/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSL "https://github.com/addisonhuddy/kfuse/releases/download/v${VER}/kfuse_${VER}_linux_${ARCH}.tar.gz" | tar xz
./kfuse --help
```

Release archives include `LICENSE`, `NOTICE`, and `THIRD_PARTY_LICENSES.md`,
and are covered by `checksums.txt`. Releases after `v0.1.0` also ship an SPDX
SBOM per archive and a Sigstore-signed build provenance attestation; verify a
download with:

```sh
gh attestation verify kfuse_${VER}_linux_${ARCH}.tar.gz --repo addisonhuddy/kfuse
```

#### install.sh

From a checkout, install the latest release with checksum verification:

```sh
./install.sh --release
./install.sh --release --version v0.1.0
./install.sh --release --dest "$HOME/.local/bin"
```

The installer uses `/usr/local/bin` when writable, otherwise `~/.local/bin`, and
fails on release download or checksum errors instead of falling back to a source
build.

### Docker

```sh
docker run --rm --privileged --device /dev/fuse \
  addisonhuddy/kfuse:latest --help
```

Tags: `latest`, `vMAJOR.MINOR` (e.g. `v0.1`), and the exact version (e.g.
`0.1.0`); see [Docker Hub](https://hub.docker.com/r/addisonhuddy/kfuse/tags).
The image only ships the binary;
to actually mount a session you still need Kafka and S3 (or the local stack from
the demo below).

### Source

To build and install the CLI from source, use the Go version in the `go`
directive of [go.mod](go.mod) or newer. On Linux:

```sh
./install.sh
```

To build on another host for a Linux sandbox:

```sh
GOOS=linux GOARCH=amd64 ./install.sh --output dist/kfuse
```

Native `./install.sh` installs from source. Cross builds are written to a file,
not installed on this host. Use `GOARCH=arm64` for an arm64 runtime.

## Examples

### Local demo

The quickest credential-free path. This starts Apache Kafka in single-node
KRaft mode and MinIO, builds a demo container, and runs a real FUSE walkthrough
covering file operations, persistence, resume, checkpointing, and branching.

From the repository root on a Linux Docker host with Compose and `/dev/fuse`:

```sh
make local-demo
```

Successful output ends with:

```text
COMPLETE (42/42)
```

For an interactive mount instead of the scripted walkthrough:

```sh
./examples/local/demo.sh --shell
```

Type `exit` to leave the interactive mount. Stop Kafka and MinIO when finished:

```sh
make local-down
```

Stopping the stack preserves its data volumes. The stack uses dummy credentials
and unauthenticated Kafka; run it only on a trusted development host, not a
publicly reachable server. The privileged demo container is not a security
boundary for untrusted agent code.

See the [local demo guide](examples/local/README.md) for direct CLI use,
additional walkthrough modes, and running the same demo against hosted services.

### Cloud demo: E2B

See a workspace outlive its sandbox. The E2B demo writes and checkpoints a file,
removes the original sandbox, then creates independent branches in new
sandboxes and verifies replay from both a checkpoint and a branch's latest state.

On your development machine, you need:

- Go (the version in [go.mod](go.mod) or newer) on `PATH`;
- uv and Python 3.10 or newer;
- an E2B API key;
- a Kafka broker and S3-compatible bucket reachable from the E2B sandboxes.
  Confluent Cloud and AWS S3 are the preferred hosted setup.

Create a repository-root `.env` from the template if you do not already have one:

```sh
cp -n .env.example .env
```

Fill in `BOOTSTRAP_SERVER`, the Kafka SASL credentials for your hosted broker,
`S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, and `S3_BUCKET`. Uncomment and set
`E2B_KEY`. For Confluent Cloud, use a **cluster-level Kafka API key**, not an
organization/global key. Never commit `.env`.

Then run:

```sh
cd examples/e2b-sandbox
uv run main.py
```

The launcher reads the repository-root `.env` (non-empty exported variables take
precedence), builds a Linux binary, and uploads it to the sandboxes. No custom
E2B template or local Docker/FUSE setup is needed. A successful run prints a
branch summary and ends with `COMPLETE`.

For an interactive workspace, run from the same directory:

```sh
uv run main.py --repl
```

Use `:restart` to move the session to a fresh sandbox, and `exit` to quit.
The demo creates billable cloud resources and retains session history in Kafka
and S3 after closing its sandboxes. See the [E2B demo guide](examples/e2b-sandbox/README.md)
for credentials, session handoff, and troubleshooting.

## Use the CLI

A **lower** is the read-only base directory; a **session** is a mutable view over
it. After building the CLI on Linux, configure storage from the repository root.
For local Kafka and MinIO:

```sh
make local-up
set -a; . examples/local/local.env; set +a
```

For hosted storage, fill in `.env` using [.env.example](.env.example), then export
it with `set -a; . ./.env; set +a`.

**Configuration loading:** the `kfuse` binary and Docker image read only exported
environment variables and never load `.env`. `examples/local/run.sh`, `up.sh`,
and `demo.sh` load the repo-root `.env` (or `KFUSE_ENV_FILE`) via
`examples/local/env.sh`; the E2B example uses the same loading rules. Already-
exported variables take precedence over `.env` values. There are no alias names:
use exactly the names in `.env.example`.

Create a disposable lower and prove that a write survives remounting. Keep your
shell outside the lower directory to avoid a busy unmount:

```sh
BASE=$(mktemp -d /tmp/kfuse-lower.XXXXXX)
LOWER_ID=$(basename "$BASE")
SID=$(./kfuse session new --lower "$BASE" --lower-id "$LOWER_ID")

./kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
printf 'hello\n' > "$BASE/hello.txt"
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"

./kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
cat "$BASE/hello.txt"
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"
```

The file should still contain `hello`. Save the lower directory, lower ID,
session ID, and storage configuration to resume later. On another host, supply
an equivalent base tree with the same lower ID; the session ID alone does not
capture the base contents. Never share a lower ID between unrelated base trees.

Checkpoint the session, then branch it and mount the child alongside a copy of
the base tree:

```sh
./kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
printf 'v2\n' > "$BASE/hello.txt"
OFFSET=$(./kfuse checkpoint --lower "$BASE" --lower-id "$LOWER_ID")   # asks the live mount to flush
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"

CHILD=$(./kfuse session branch "$SID" --to "$OFFSET" --lower "$BASE" --lower-id "$LOWER_ID")
./kfuse mount "$CHILD" --lower "$BASE" --lower-id "$LOWER_ID"
cat "$BASE/hello.txt"            # v2 — inherited from the parent at $OFFSET
printf 'child\n' > "$BASE/hello.txt"   # only the child sees this
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"
```

If you started the local stack, stop it with `make local-down` when finished.

## CLI reference

Every command takes `--lower/-l <dir>` (default: cwd) and `--lower-id <id>`
(default: `KF_LOWER_ID`, else minted and persisted in the lower). Run
`kfuse <command> --help` for details.

| Command | Purpose |
|---|---|
| `kfuse session new` | Create a session over the lower and print its ID. |
| `kfuse session ls` | List sessions recorded for this lower. |
| `kfuse session select <sid>` | Set the default session for `kfuse mount` (local state only). |
| `kfuse session branch <parent> [--to N]` | Fork a child session from the parent at offset `N` (default: current committed tail) and print its ID. |
| `kfuse mount [sid] [--foreground]` | Mount the session over the lower; replays from the latest checkpoint plus newer Kafka records and takes the writer lease. |
| `kfuse status` | Exit 0 when a mount is live; print the session and offsets. |
| `kfuse checkpoint [sid]` | Flush a state image to S3 and print the covered Kafka offset. |
| `kfuse umount` | Unmount and release the writer lease. |
| `kfuse version` | Print the build version. |

Configuration is environment-only; the full variable list is in
[.env.example](.env.example). Local per-lower state (session marker, daemon
pidfile/socket, logs) lives under `KF_STATE_DIR`, else `$XDG_STATE_HOME/kfuse`,
else `~/.kfuse`.

## How it works

- The lower tree remains read-only. The session's in-memory upper supplies the
  mutable overlay: new nodes, sparse file extents, attribute overrides,
  redirects, and whiteouts.
- File bytes are uploaded to content-addressed S3 objects before the mutation
  event is appended to Kafka. A Kafka append with `acks=all` is the commit
  point; the in-memory upper is updated only after that append succeeds.
- Checkpoints write a serialized state image to S3 so resume can start from the
  image and replay only newer Kafka records.
- A branch reconstructs the parent's state at a selected committed offset, then
  records the child session's lineage and starts a new event stream.
- A session has one intended writer at a time, coordinated by a best-effort S3
  lease. Mount normally refuses a second healthy writer while a live lease
  exists, but the check-and-write lease flow is not an atomic distributed lock.
  If the current writer detects that it lost the lease, later mutations return
  `EROFS`. A frozen writer can miss that signal until its next renewal, so
  split-brain behavior around lease expiry remains an operational limitation,
  not a correctness guarantee.

## Limits

- Linux FUSE only; release builds target `linux/amd64` and `linux/arm64`.
- Supported filesystem behavior covers regular files, directories, symlinks,
  rename, truncate, attributes, and fsync. Hardlinks, xattrs, ACLs, advisory
  locks, and device/socket/FIFO files are outside the current scope.
- Branching from an old offset requires the parent records and any needed state
  image to remain available. Kafka retention and retained S3 objects bound how
  far back a branch can go; verify your topic's configured retention rather
  than assuming unlimited history.
- Blob cleanup and deletion of retained remote history are not automatic; plan
  bucket lifecycle and cost controls without expiring objects needed by sessions.
- Before relying on persistent sessions, see [OPERATIONS.md](OPERATIONS.md) for
  the Kafka topic and S3 bucket settings (no compaction, fixed partition count,
  retention, replication, lifecycle, least-privilege credentials) that keep
  history replayable.

## Why kfuse

kfuse sits between "snapshot the whole VM" and "commit to git":

| | kfuse | git worktree / commit | overlayfs / container layers | VM or sandbox snapshot |
|---|---|---|---|---|
| Captures untracked files, build output, generated data | yes | only what you `git add` | yes | yes |
| Survives the host/sandbox dying | yes (Kafka + S3) | only after push | no (host-local) | yes |
| Branch at any past point without a prior commit | yes (any committed offset) | needs a commit | no | needs a snapshot |
| Resume on a different host | yes (same base tree + lower ID) | clone/pull | no | same hypervisor/provider |
| Granularity | per-write event | per-commit | per-layer | whole image |
| Needs | Linux FUSE, Kafka, S3 | git | kernel overlayfs | provider tooling |

It is a good fit when many agents (or many attempts by one agent) need cheap,
independent, durable copies of a working directory that outlive the compute
they run on. It is not a replacement for git as a source of truth, and it is
not a sandbox (see [Security and concurrency boundaries](#security-and-concurrency-boundaries)).

## FAQ

**Does kfuse need Confluent Cloud or AWS?** No. Any Kafka broker and any
S3-compatible object store work; the local demo uses Apache Kafka in KRaft mode
and MinIO. Confluent Cloud and AWS S3 are simply the hosted setup the examples
are tested against.

**Can I run it on macOS or Windows?** Not natively — the mount needs Linux FUSE.
Use a Linux VM, a privileged Linux container, or the E2B demo, where the mount
runs inside a cloud sandbox.

**Where does the base (lower) tree come from on a new host?** You bring it.
kfuse records mutations relative to the lower, not the lower itself, so the
resuming host needs an equivalent base tree under the same lower ID (a git
checkout at the same commit, a container image layer, and so on).

**How big can a session get?** File bytes go to S3 as content-addressed blobs,
so data volume is bounded by your bucket, not memory. The upper's metadata
(nodes, extents, attributes) is held in memory on the mounting host.

**What happens if two hosts mount the same session?** The second mount is
refused while the first holds a live lease. The lease is best-effort, not an
atomic lock; see [How it works](#how-it-works) for the split-brain caveat.

**How do I delete a session?** There is no `session rm` yet. Remote history is
retained until Kafka retention and S3 lifecycle policy expire it; see
[OPERATIONS.md](OPERATIONS.md).

## Roadmap

Tracked in [GitHub issues](https://github.com/addisonhuddy/kfuse/issues).
Before a beta, the main themes are:

- a stored-format compatibility guarantee (versioned state images and events);
- stronger writer fencing than the best-effort S3 lease;
- session lifecycle commands (`session rm`, blob garbage collection);
- xattrs and hardlinks in the overlay.

## Troubleshooting

| Symptom | Next check |
|---|---|
| `missing required env: ...` | Fill `.env` for hosted examples or export the variables required by the CLI. The local demo supplies its own configuration. |
| SASL error `[58]` | Use a Confluent Cloud **Kafka API key**, not a Global/org key. |
| `mount never became live` | Use Linux FUSE support or a privileged Linux container with `/dev/fuse`. |
| `session locked` | Another live mount owns the session lease; unmount it or wait for lease expiry. |
| `Device or resource busy` on unmount | Close files and move shells/processes out of the mounted lower before `kfuse umount`. |
| `stage=auth` in launcher output | Kafka SASL or S3 credentials rejected; the launcher prints the next action — see [examples/local/README.md](examples/local/README.md#failure-stages). |
| `stage=broker` in launcher output | Broker unreachable; check `BOOTSTRAP_SERVER`, egress, and `KAFKA_TLS` — see the failure-stages table. |
| `stage=bucket` in launcher output | S3 bucket missing or wrong region/endpoint — see the failure-stages table. |
| `stage=topic` in launcher output | `KAFKA_TOPIC` missing; create it or grant Create/Describe ACLs — see the failure-stages table. |
| `stage=fuse` in launcher output | No usable FUSE on the Docker host — see the failure-stages table. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and contribution
instructions. Security reports go through [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
