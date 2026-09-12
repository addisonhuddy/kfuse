# local — Apache Kafka + MinIO

A credential-free development stack for the real kfuse binary. It runs:

- `apache/kafka` in single-node KRaft mode with its host listener bound to
  `127.0.0.1:9092`, with an unauthenticated plaintext listener;
- `minio/minio` with host ports bound to `127.0.0.1:9000` (console on
  `127.0.0.1:9001`).

Hosted Confluent Cloud + AWS S3 remains the preferred kfuse deployment. This
stack exists for local development and for validating that the production
Kafka/S3 code paths work against other providers. It runs a single broker
(replication factor 1) and is not a durability reference — before deploying
hosted storage, see [OPERATIONS.md](../../OPERATIONS.md) for the topic and
bucket settings that keep session history replayable.

## Start

```sh
make local-up
set -a; . examples/local/local.env; set +a
```

`up.sh` waits for both services and creates `S3_BUCKET` (`kfuse-local` by
default). `local.env` contains only dummy local credentials; it is safe to
commit.

To run the full functional demo in a privileged Linux Docker container:

```sh
make local-demo
```

The demo uses `container.env`: the demo container joins the private
`kfuse-local` Docker network, Kafka advertises `kafka:9094` on it, and MinIO is
reached at `http://minio:9000`. This still requires a Linux Docker host with
`/dev/fuse`; Docker Desktop mount support is not currently validated.

## Walkthrough modes

`run.sh` builds the image and runs the walkthrough in a privileged container.
It exits non-zero on the first failing step and prints
`COMPLETE (42/42)` when everything works.

```sh
./examples/local/run.sh               # needs hosted credentials from .env
./examples/local/run.sh --shell       # live mount + bash
./examples/local/run.sh --cross-host  # two sandboxes, one session
./examples/local/run.sh --local --creds examples/local/container.env
```

The walkthrough covers creating and mounting a session, writes and appends,
unlinking files, directories, symlinks, attributes, rename, sparse
copy-on-write, resume, branching, checkpointing, and cross-host lease
conflicts. The `--local` flag joins the container to the private `kfuse-local`
Docker network; `--creds <file>` selects another credentials file, while
exported environment values take precedence.

## Security model

This stack is for development only. Kafka is plaintext and unauthenticated,
and MinIO uses the committed dummy credentials in `local.env`. Nothing is
published beyond loopback (`127.0.0.1`) and the private Docker network, so
other hosts on the LAN cannot reach it. To expose it deliberately, edit the
`ports:` host IPs, and never do so with these credentials.

The image can also be run directly:

```sh
docker build -f examples/local/Dockerfile -t kfuse-local-demo .
docker run --rm --privileged --device /dev/fuse \
  --env-file <creds.env> kfuse-local-demo          # also: shell | bash
```

## Preflight

Before building or mounting anything, `run.sh` runs read-only checks
(`--skip-preflight` disables them):

- **host-prereqs** — the docker CLI exists and the daemon answers
  `docker info` within `KFUSE_PREFLIGHT_TIMEOUT` (15s).
- **fuse** — FUSE is verified where the mount actually runs: on a Linux
  Docker host that is this machine, `/dev/fuse` must exist (override the
  path with `KFUSE_FUSE_DEV`); otherwise (macOS, WSL, Docker Desktop) the
  engine itself is probed with a read-only `docker run --device /dev/fuse`
  on `busybox:stable`, which is the only image pull preflight performs.
- **config** — required variables are present and non-placeholder, and
  `KAFKA_TLS`, `S3_PATH_STYLE`, `KAFKA_PARTITIONS`, `S3_ENDPOINT`, the
  `KAFKA_SASL_*` pair, and `KF_LOWER_ID` match the shapes
  `internal/config` enforces.

The only remote writes the demo makes are the session records it commits
under `S3_PREFIX` and the Kafka topic events for those sessions.

## Failure stages

On failure the launchers print a `stage=<name>` line naming the failing
layer and the next action:

| stage | meaning | next action | log |
|---|---|---|---|
| `host-prereqs` | docker CLI/daemon/compose missing | install or start Docker | terminal output |
| `config` | missing/placeholder/malformed env | fix the named variables in `.env` (see `.env.example`) | terminal output |
| `auth` | Kafka SASL or S3 credential rejected | use a cluster Kafka API key (not Global); check S3 key permissions | `/tmp/kfuse-demo/*.log` in the container |
| `bucket` | S3 bucket missing | create `S3_BUCKET` or fix name/`S3_REGION`/`S3_ENDPOINT` | same |
| `lease` | another live mount holds the session | `kfuse umount` there or wait for the lease TTL | same |
| `topic` | Kafka topic missing | create `KAFKA_TOPIC` with `KAFKA_PARTITIONS` partitions or grant ACLs | same |
| `broker` | cannot reach the broker | check `BOOTSTRAP_SERVER`, egress, `KAFKA_TLS` | same |
| `mount`/`fuse` | no usable FUSE | privileged Linux container with `--device /dev/fuse` | `/tmp/kfuse-demo/mount-*.log` |
| `unknown` | unclassified | inspect the log | same |

Daemon logs live at `$KF_STATE_DIR/<lower-id>/daemon.log` inside the
container (`/var/lib/kfuse` by default). For the local stack itself use
`docker compose -f examples/local/docker-compose.yaml logs kafka|minio`.
We never recommend disabling TLS or checksum verification as a fix.

## Use kfuse directly

```sh
go build -o kfuse ./cmd/kfuse
BASE=/tmp/kfuse-local-lower
mkdir -p "$BASE"
printf 'base\n' > "$BASE/base.txt"

SID=$(./kfuse session new --lower "$BASE" --lower-id local-lower)
./kfuse mount "$SID" --lower "$BASE" --lower-id local-lower --foreground
```

Use another terminal to write through the mount, then unmount or stop the
foreground mount. Named volumes persist both Kafka records and MinIO objects
across stack restarts.

## Unmount

Inside `--shell`, `exit` unmounts cleanly; against a standalone mount use
`kfuse umount`. To stop the local stack without deleting data:

```sh
make local-down
```

## Busy mount

`Device or resource busy` on unmount means a shell or process still has the
lower open: move shells out of the directory, check `fuser -vm /work/lower`,
and only as a last resort `fusermount3 -uz`.

## Recovery

Sessions are durable: re-mount the same session id to resume after a crash
or a stopped sandbox. If another live mount holds the lease (`stage=lease`),
unmount it there or wait for the lease TTL.

## Destructive reset

```sh
./examples/local/down.sh --volumes --yes
```

This deletes both named volumes — all local Kafka records and every local
MinIO object — and asks for interactive confirmation unless `--yes` is
given. It never touches hosted buckets or topics. Kafka runs with a fixed
local cluster ID and MinIO keeps the bucket in a named volume; deleting
only one volume creates an inconsistent local history and should be
treated as a fresh environment.

## Configuration shape

The generated environment is intentionally ordinary kfuse configuration:

```sh
BOOTSTRAP_SERVER=127.0.0.1:9092
KAFKA_TLS=false
S3_ENDPOINT=http://127.0.0.1:9000
S3_PATH_STYLE=true
S3_REGION=us-east-1
S3_ACCESS_KEY=kfuse
S3_SECRET_KEY=kfuse-local-secret
S3_BUCKET=kfuse-local
```

No `KAFKA_SASL_*` values are set because the local listener is
unauthenticated. The same `S3_ENDPOINT`/`S3_PATH_STYLE` mechanism works for
other S3-compatible stores; the same optional `KAFKA_SASL_*` pair works for
brokers that require authentication.
