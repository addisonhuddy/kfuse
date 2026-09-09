# local — Apache Kafka + MinIO

A credential-free development stack for the real kfuse binary. It runs:

- `apache/kafka` in single-node KRaft mode on `127.0.0.1:9092`, with an
  unauthenticated plaintext listener;
- `minio/minio` on `127.0.0.1:9000` (console on `127.0.0.1:9001`).

Hosted Confluent Cloud + AWS S3 remains the preferred kfuse deployment. This
stack exists for local development and for validating that the production
Kafka/S3 code paths work against other providers.

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

The demo uses `container.env`: Kafka advertises a second listener on
`host.docker.internal:9094`, and MinIO is reached through
`host.docker.internal:9000`. This still requires a Linux Docker host with
`/dev/fuse`; Docker Desktop mount support is not currently validated.

## Walkthrough modes

`run.sh` builds the image and runs the walkthrough in a privileged container.
It exits non-zero on the first failing step and prints
`DEMO PASS (42/42 steps)` when everything works.

```sh
./examples/local/run.sh               # needs hosted credentials from .env
./examples/local/run.sh --probe       # FUSE + S3 checks, no Kafka
./examples/local/run.sh --shell       # live mount + bash
./examples/local/run.sh --cross-host  # two sandboxes, one session
./examples/local/run.sh --local --creds examples/local/container.env
```

The walkthrough covers creating and mounting a session, writes and appends,
unlinking files, directories, symlinks, attributes, rename, sparse
copy-on-write, resume, branching, checkpointing, and cross-host lease
conflicts. The `--local` flag lets the container reach host-published local
services; `--creds <file>` selects another credentials file, while exported
environment values take precedence.

The image can also be run directly:

```sh
docker build -f examples/local/Dockerfile -t kfuse-local-demo .
docker run --rm --privileged --device /dev/fuse \
  --env-file <creds.env> kfuse-local-demo          # also: probe | shell | bash
```

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

## Stop

```sh
make local-down
```

To delete both local volumes and all local records/objects:

```sh
./examples/local/down.sh --volumes
```

Kafka runs with a fixed local cluster ID and MinIO keeps the bucket in a named
volume. Deleting only one volume creates an inconsistent local history and
should be treated as a fresh environment.

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
