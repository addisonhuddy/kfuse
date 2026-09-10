# kfuse

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml/badge.svg)](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml)

kfuse is a branching overlay filesystem designed for agents, backed by Kafka
and S3. Mount a session over a base directory; mutations commit to Kafka, file
bytes land in S3, and the workspace can pause, resume, and branch across hosts.

## Getting Started





Start with one of the two demos:

| Demo | What you'll see | What you need |
|---|---|---|
| [Local](#local-demo) | Write files, resume a session, checkpoint, and branch using local Kafka and MinIO | Linux, Docker with Compose, and `/dev/fuse`; no cloud credentials |
| [E2B](#cloud-demo-e2b) | Persist a workspace beyond its original sandbox and run independent branches in new sandboxes | Go, uv, an E2B account, and hosted Kafka/S3 credentials; no local Docker or FUSE required |

Both demos currently run from a source checkout:

```sh
git clone https://github.com/addisonhuddy/kfuse.git
cd kfuse
```

### Binary

TODO LATER: add versioned binary installation instructions after the first release.
For now, use the demos below or build from source.

### Docker

TODO LATER: publish the Docker image and add versioned pull/run instructions.
The local demo builds its own image from the checkout; no published image is needed.

### Source

To build the CLI yourself, use Go 1.26.4 or newer. On Linux:

```sh
go build -o kfuse ./cmd/kfuse
./kfuse --help
```

To build on another host for a Linux sandbox:

```sh
GOOS=linux GOARCH=amd64 go build -o kfuse ./cmd/kfuse
```

Use `GOARCH=arm64` for an arm64 runtime. The machine hosting the mount needs
Linux, `/dev/fuse`, and `fusermount3`. macOS and Windows cannot host a kfuse
mount natively; use E2B or a Linux VM. Docker Desktop mount support is not
currently validated.

You do not need to build manually before running either demo: the local demo
builds Go inside Docker, and the E2B launcher cross-compiles the binary itself.

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

- Go 1.26.4 or newer on `PATH`;
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
it with `set -a; . ./.env; set +a`. The CLI reads exported environment variables;
it does not load `.env` automatically.

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
If you started the local stack, stop it with `make local-down` when finished.

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

## Troubleshooting

| Symptom | Next check |
|---|---|
| `missing required env: ...` | Fill `.env` for hosted examples or export the variables required by the CLI. The local demo supplies its own configuration. |
| SASL error `[58]` | Use a Confluent Cloud **Kafka API key**, not a Global/org key. |
| `mount never became live` | Use Linux FUSE support or a privileged Linux container with `/dev/fuse`. |
| `session locked` | Another live mount owns the session lease; unmount it or wait for lease expiry. |
| `Device or resource busy` on unmount | Close files and move shells/processes out of the mounted lower before `kfuse umount`. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and contribution
instructions. Security reports go through [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
