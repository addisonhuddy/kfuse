# kfuse

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml/badge.svg)](https://github.com/addisonhuddy/kfuse/actions/workflows/ci.yml)

A branching overlay filesystem for agents. Mount a session over
a base directory; mutations commit to Kafka, file bytes land in S3, and the
workspace can pause, resume, and branch across hosts.

## Try it

The supported first run today is the functional demo. It uses the source
checkout, builds a Linux demo image, mounts a real FUSE filesystem, and checks
persistence, resume, branching, and checkpointing against hosted Kafka (Confluent Cloud)
and AWS S3.

```sh
git clone https://github.com/addisonhuddy/kfuse.git
cd kfuse
cp .env.example .env       # fill in Kafka and AWS values first
./examples/functional-demo/run.sh
```

The demo prints progress for each check and ends with:

```text
DEMO PASS (42/42 steps)
```

Requirements for this path:

- a source checkout;
- a Linux Docker environment that can run privileged containers with `/dev/fuse`;
- a Confluent Cloud cluster and a cluster-level Kafka API key;
- an S3 bucket and credentials with read/write access to the configured prefix.

The launcher loads the repository-root `.env` automatically; exported
environment variables take precedence. `--creds <file>` selects another file.

`--probe` skips Kafka writes, but it is still a cloud-backed check: the launcher
requires the same configuration and the probe writes and reads an S3 blob.

For a live mount instead of the scripted checks, run:

```sh
./examples/functional-demo/run.sh --shell
```

## Mount your own workspace

Build the Linux binary from source, configure storage, then use explicit session
and lower IDs. A **lower** is the read-only base directory; a **session** is one
mutable view over that lower.

### 1. Build the binary

A Go toolchain is required only when building from source. Use Go 1.26.4 or
newer.

On Linux:

```sh
go build -o kfuse ./cmd/kfuse
# or: ./install.sh --source
```

From another development host, cross-compile the binary that will run in the
Linux sandbox:

```sh
GOOS=linux GOARCH=amd64 go build -o kfuse ./cmd/kfuse
# for arm64 sandboxes: GOOS=linux GOARCH=arm64 ...
```

The runtime host needs Linux with FUSE support (`/dev/fuse` and `fusermount3`).
macOS and Windows cannot host a kfuse mount natively; use a Linux sandbox or VM.
Docker support is limited to Linux container environments that can run a
privileged container with `/dev/fuse`. Docker Desktop on macOS and Windows is
not currently validated for hosting the mount.

### 2. Configure storage

Copy `.env.example` to `.env` and replace every placeholder. Use the canonical
variable names shown there. For a Confluent Cloud cluster, use a **Kafka API
key** from Cluster → API keys, not a Global/org key; the wrong key type fails
SASL with `[58]`.

Example launchers load `.env` themselves. The `kfuse` binary reads exported
environment variables:

```sh
set -a && . ./.env && set +a
```

Choose a stable `KF_LOWER_ID` for the logical base. Use a unique ID per base
tree rather than a generic value shared by unrelated directories. To resume on
another host, use the same lower ID and an equivalent base tree; the session ID
alone does not describe the base contents.

### 3. Prove persistence and branching

Run these commands from a shell whose working directory is outside the mounted
lower. Keeping the shell outside the mount avoids a busy unmount.

```sh
BASE=/tmp/kfuse-lower
LOWER_ID=my-first-lower
mkdir -p "$BASE"
printf 'base\n' > "$BASE/base.txt"

SID=$(./kfuse session new --lower "$BASE" --lower-id "$LOWER_ID")

./kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
printf 'hello\n' > "$BASE/hello.txt"
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"

./kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
cat "$BASE/hello.txt"                         # hello
OFFSET=$(./kfuse checkpoint --lower "$BASE" --lower-id "$LOWER_ID")
CHILD=$(./kfuse session branch "$SID" --to "$OFFSET" \
  --lower "$BASE" --lower-id "$LOWER_ID")
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"

./kfuse mount "$CHILD" --lower "$BASE" --lower-id "$LOWER_ID"
printf 'child\n' >> "$BASE/hello.txt"
cat "$BASE/hello.txt"
# hello
# child
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"

./kfuse mount "$SID" --lower "$BASE" --lower-id "$LOWER_ID"
cat "$BASE/hello.txt"                         # hello
./kfuse umount --lower "$BASE" --lower-id "$LOWER_ID"
```

The parent session keeps its checkpointed contents while the child evolves
independently. Save the lower ID, session IDs, storage prefix, and the exact
mount command if you need to resume later.

`kfuse session select` records a local convenience default only. Use an explicit
session ID when moving across hosts or scripts.

## Run in a locally built image

The release Dockerfile builds a Linux runtime image containing `kfuse` and
`fuse3`. Build it locally rather than assuming a published image tag exists:

```sh
docker build -t kfuse:local .
```

Create the session, then start a named foreground container. The mount lives in
the container's mount namespace; use `docker exec` to inspect or modify it. Do
not assume the mounted view appears at the host's bind-mount path.

```sh
BASE=/tmp/kfuse-lower
LOWER_ID=my-first-lower

SID=$(docker run --rm --env-file .env \
  -v "$BASE:/work/lower" \
  kfuse:local session new --lower /work/lower --lower-id "$LOWER_ID" | tail -n1)

docker run -d --name kfuse-mount --privileged --device /dev/fuse \
  --env-file .env \
  -v "$BASE:/work/lower" -v kfuse-state:/var/lib/kfuse \
  kfuse:local mount --foreground "$SID" --lower /work/lower --lower-id "$LOWER_ID"

docker exec kfuse-mount sh -c 'printf "hello\n" > /work/lower/hello.txt'
docker exec kfuse-mount kfuse umount --lower /work/lower --lower-id "$LOWER_ID"
docker rm kfuse-mount
```

If startup fails, inspect `docker logs kfuse-mount` before removing the named
container.

The named volume preserves the local daemon state used by `umount`, `status`,
and the control socket. Remote session history remains in Kafka and S3. To
resume the session later, keep the same lower ID and storage configuration and
use the saved session ID.

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

- Linux FUSE only; release builds target `linux/amd64` and `linux/arm64`. Build
  the binary or image locally unless a published release supplies a verified
  artifact for your platform.
- Supported filesystem behavior covers regular files, directories, symlinks,
  rename, truncate, attributes, and fsync. Hardlinks, xattrs, ACLs, advisory
  locks, and device/socket/FIFO files are outside the current scope.
- Branching from an old offset requires the parent records and any needed state
  image to remain available. Kafka retention and retained S3 objects bound how
  far back a branch can go.
- Blob cleanup and deletion of retained remote history are not automatic; plan
  bucket lifecycle and cost controls accordingly.

## More examples and references

| Path | What it shows |
|---|---|
| [`examples/functional-demo`](examples/functional-demo/README.md) | Scripted POSIX walkthrough, interactive shell, and cross-container session conflict check |
| [`examples/best-of-n`](examples/best-of-n/README.md) | Checkpoint and parallel hypothesis branches |
| [`examples/e2b-sandbox`](examples/e2b-sandbox/README.md) | Resume and branching across disposable E2B sandboxes |
| [`examples/README.md`](examples/README.md) | Credentials, aliases, and expected pass output for all demos |
| [`.env.example`](.env.example) | Canonical configuration names and defaults |
| [`docs/design.md`](docs/design.md) | Design model and shipped implementation notes |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Build, test, integration-test, and contribution workflow |

## Troubleshooting

| Symptom | Next check |
|---|---|
| `missing required env: ...` | Fill `.env` or export the canonical variables required by the entry point. |
| SASL error `[58]` | Use a Confluent Cloud **Kafka API key**, not a Global/org key. |
| `mount never became live` | Use Linux FUSE support or a privileged Linux container with `/dev/fuse`. |
| `session locked` | Another live mount owns the session lease; unmount it or wait for lease expiry. |
| `Device or resource busy` on unmount | Close files and move shells/processes out of the mounted lower before `kfuse umount`. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security reports go through
[SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
