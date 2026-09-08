---
name: testing-local-mount
description: Stand up a credential-free local stack (Apache Kafka KRaft + MinIO) so an unmodified kfuse binary can do a real FUSE mount, and hammer it for concurrency bugs.
---

# Testing kfuse with a real mount, without cloud credentials

kfuse normally needs hosted Confluent + AWS S3 (a repo-root `.env`, see
CONTRIBUTING.md). When no `.env` / secrets exist you can still get a **real**
mount by pointing the unmodified binary at local stand-ins. No source change is
required: `internal/kafkalog` only enables SASL when `KF_KAFKA_SASL_USERNAME`
is non-empty and only enables TLS when `KF_KAFKA_TLS=true`, and
`internal/s3util.NewClient` builds a plain AWS SDK client, so DNS + a trusted CA
are enough to redirect S3.

Both stand-ins are pulled from Docker Hub, so on a network-restricted box the
allowlist must include `registry-1.docker.io`, `auth.docker.io` and
`production.cloudflare.docker.com` (otherwise `docker pull` dies with
`Get "https://registry-1.docker.io/v2/": EOF`); request it before planning a
mount run.

## Kafka — official Apache Kafka in KRaft mode (use this, not Redpanda)

`apache/kafka:latest` needs a JAAS file **and** `KAFKA_OPTS`; its `configure`
script exits with `!1: unbound variable` if `KAFKA_OPTS` is unset while a
`SASL_*` advertised listener is present.

```sh
cat > /tmp/jaas/kafka_server_jaas.conf <<'EOF'
KafkaServer {
  org.apache.kafka.common.security.plain.PlainLoginModule required
  username="kfuse" password="kfusepass123" user_kfuse="kfusepass123";
};
EOF
docker run -d --name kafka --network host \
  -v /tmp/jaas:/etc/kafka/jaas:ro \
  -e KAFKA_OPTS='-Djava.security.auth.login.config=/etc/kafka/jaas/kafka_server_jaas.conf' \
  -e CLUSTER_ID=5L6g3nShT-eMCtKzzX86sw -e KAFKA_NODE_ID=1 \
  -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS='SASL_PLAINTEXT://:9092,CONTROLLER://:9093' \
  -e KAFKA_ADVERTISED_LISTENERS='SASL_PLAINTEXT://127.0.0.1:9092' \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP='CONTROLLER:PLAINTEXT,SASL_PLAINTEXT:SASL_PLAINTEXT' \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS='1@localhost:9093' \
  -e KAFKA_INTER_BROKER_LISTENER_NAME=SASL_PLAINTEXT \
  -e KAFKA_SASL_ENABLED_MECHANISMS=PLAIN \
  -e KAFKA_SASL_MECHANISM_INTER_BROKER_PROTOCOL=PLAIN \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 -e KAFKA_NUM_PARTITIONS=8 \
  apache/kafka:latest
```

Then `KF_KAFKA_TLS=false`, `KF_KAFKA_SASL_USERNAME=kfuse`,
`KF_KAFKA_SASL_PASSWORD=kfusepass123`, `KF_KAFKA_BROKERS=127.0.0.1:9092`.
`kfuse session new` may print `warning: ensure topic: ... %!w(<nil>)` — that
message is a formatting artifact, the topic is created fine.

## S3 — MinIO impersonating the real S3 endpoint

`s3util.NewClient` sets no `BaseEndpoint`, so redirect by DNS + TLS instead of
patching code:

```sh
openssl req -x509 -newkey rsa:2048 -sha256 -days 30 -nodes \
  -keyout certs/private.key -out certs/public.crt \
  -subj "/CN=s3.us-east-1.amazonaws.com" \
  -addext "subjectAltName=DNS:s3.us-east-1.amazonaws.com,DNS:*.s3.us-east-1.amazonaws.com"
docker run -d --name mio --network host \
  -e MINIO_ROOT_USER=kfuseakid -e MINIO_ROOT_PASSWORD=kfusesecret123 \
  -e MINIO_DOMAIN=s3.us-east-1.amazonaws.com \
  -v $PWD/certs:/certs:ro -v /tmp/miodata:/data \
  minio/minio:latest server /data --address :443 --certs-dir /certs
echo "127.0.0.1 s3.us-east-1.amazonaws.com <bucket>.s3.us-east-1.amazonaws.com" | sudo tee -a /etc/hosts
```

Export `SSL_CERT_FILE=<abs path>/certs/public.crt` for every kfuse process (Go
honours it, so no system trust-store change is needed), plus `AWS_REGION=us-east-1`,
`AWS_ACCESS_KEY_ID=kfuseakid`, `AWS_SECRET_ACCESS_KEY=kfusesecret123`,
`KF_BLOB_BUCKET=<bucket>`. Create the bucket once (any S3 client, or a throwaway
`main.go` **inside the module** — `internal/...` cannot be imported from outside).

Bucket-creation gotchas: a `minio/mc` docker container does **not** inherit the
host's `/etc/hosts`, so `mc` silently resolves `s3.us-east-1.amazonaws.com` to
real AWS and fails with "The AWS Access Key Id you provided does not exist"
even with `--add-host` (virtual-host-style redirects can still leak). The
simplest reliable method is curl's built-in SigV4 from the host:

```sh
curl -s -X PUT --cacert certs/public.crt --user kfuseakid:kfusesecret123 \
  --aws-sigv4 "aws:amz:us-east-1:s3" https://s3.us-east-1.amazonaws.com/kfuse-blobs
```

The same curl pattern with `?list-type=2` lists objects, e.g. to prove
checkpoint state images landed for a session id. When uploading a body, send it
with `--data-binary @file`, not `--upload-file`: curl streams an
`--upload-file` body and signs the request over the *empty* payload hash, so a
SigV4-verifying endpoint answers `403 SignatureDoesNotMatch` (verified by
recomputing the signature both with the AWS SDK v4 signer and a standalone
SigV4 implementation).

## Mounting

FUSE works directly on the Devin box (`/dev/fuse`, `fusermount3`, no privileged
container needed):

```sh
export KF_STATE_DIR=/tmp/kf-harness/state KF_LOWER_ID=local-lower
SID=$(kfuse session new | tail -1)
kfuse mount "$SID" --foreground --lower /tmp/kf-harness/lower >mount.log 2>&1 &
# wait for: grep " /tmp/kf-harness/lower " /proc/mounts
kill -TERM $!   # clean unmount; fusermount3 -u <dir> to force
```

`examples/functional-demo/demo.sh` is the canonical golden path — mirror its
assertions instead of inventing new ones.

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

None for this local harness. A real cloud run needs a repo-root `.env` with
`KF_KAFKA_BROKERS`, `KF_KAFKA_SASL_USERNAME`, `KF_KAFKA_SASL_PASSWORD`,
`AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `KF_BLOB_BUCKET`
(Confluent Cloud *Kafka* API key, not an org key).
