# Operations: Kafka and S3 settings that keep history replayable

This checklist is for operators running kfuse beyond the credential-free
[local stack](examples/local/README.md). Every item below was verified against
the implementation (`internal/kafkalog`, `internal/blobstore`, `internal/session`,
`internal/registry`). Getting these wrong does not fail loudly at startup — it
makes resume and branching silently incomplete later.

## What lands where

- **Kafka** (`KAFKA_TOPIC`, default `kfuse.events`): one record per committed
  mutation, keyed by `session_id`. Only metadata and blob references travel
  through Kafka; file bytes never do.
- **S3** (`S3_BUCKET` under `S3_PREFIX`, default `kfuse/`):
  - `blobs/sha256/{aa}/{bb}/{hex}` — content-addressed file bytes; identical
    content across sessions and branches shares one object.
  - `state/sessions/{id}/image-{offset}.json` — serialized upper snapshots
    ("state images") tagged with the log offset they cover.
  - `meta/` — session records, per-lower session indexes, branch provenance,
    and writer-lease locks (`meta/sessions/{id}.lock`).

A resume loads the newest usable state image, then replays the session's Kafka
records newer than the offset that image covers. Branching replays the parent's
records up to the chosen branch offset. Both therefore depend on Kafka records
*and* the S3 objects they reference still being there.

## Checklist

| # | Setting | Required value | If wrong |
|---|---|---|---|
| 1 | `cleanup.policy` | `delete` only | Compaction keeps only the newest record per `session_id` key — all mutation history is destroyed. |
| 2 | Partition count | Fixed at creation (default `KAFKA_PARTITIONS=8`) | Repartitioning reroutes sessions; existing history becomes unreachable. |
| 3 | `retention.ms` / `retention.bytes` | Unbounded, or long enough to cover every offset you may resume or branch from | Expired records are silently skipped; resume loses mutations, old branches break. |
| 4 | `replication.factor` / `min.insync.replicas` | `≥3` / `2` for durable deployments | `acks=all` only waits for in-sync replicas; on a single broker the log is one disk. |
| 5 | S3 lifecycle rules | None that expire `blobs/`, `state/`, or `meta/` under `S3_PREFIX` | Expired objects corrupt every session that still references them. |
| 6 | Credentials | Least-privilege: runtime key cannot create topics, list the whole bucket, or delete non-lease objects | See [Least-privilege credentials](#least-privilege-credentials). |

## 1. Never enable log compaction

The event log is an append-only history. With `cleanup.policy=compact`, Kafka
retains only the newest record per key — and every kfuse record for a session
shares that session's key. The entire mutation history collapses to a single
record. Do not enable compaction on `KAFKA_TOPIC`, including via a cluster-wide
default or a broker-level `log.cleanup.policy=compact`.

Verify:

```sh
kafka-configs --bootstrap-server "$BOOTSTRAP_SERVER" \
  --command-config client.properties \
  --entity-type topics --entity-name "$KAFKA_TOPIC" \
  --describe --all
```

`cleanup.policy` must be `delete` (the Kafka default). On Confluent Cloud,
`confluent kafka topic describe "$KAFKA_TOPIC"` reports the same value.

## 2. Partition count is fixed once sessions exist

Routing is `FNV-1a(session_id) % partition_count`
(`internal/kafkalog` `PartitionFor`), computed identically by the producer and
the replayer. Kafka only ever *increases* a topic's partition count, and that
one-way change is exactly what breaks kfuse: after repartitioning, the modulo
sends a resumed session's reads to a different partition than its history was
written to. The old records still exist but are unreachable — the session
replays nothing and mounts an empty overlay, while its history sits stranded on
the original partition.

Avoid accidental changes:

- **Create the topic once, deliberately, before first use.** `kfuse` creates a
  missing topic itself (`EnsureTopic`, best-effort at `session new`, `branch`,
  and `mount` startup) using `KAFKA_PARTITIONS` — but only if the credential
  is allowed to. Prefer provisioning the topic out of band so that count is a
  decision, not a default that slipped through.
- `KAFKA_PARTITIONS` (default 8) matters only at topic-creation time. At
  runtime kfuse reads the *actual* partition count from broker metadata
  (`RefreshPartitions`) and routes by it, so a config/topic mismatch does not
  silently misroute — it just means the configured value is ignored.
- The client never relies on broker-side auto-creation
  (`Metadata.AllowAutoTopicCreation=false`); leave
  `auto.create.topics.enable=false` on the broker so a fat-fingered mount
  cannot mint a wrongly-partitioned topic. A missing topic fails fast at
  `RefreshPartitions` rather than appearing with defaults.
- Do not run `kafka-topics --alter --partitions` on an existing kfuse topic.
  To change the count deliberately, create a new topic (and a new
  `KAFKA_TOPIC`), accepting that existing sessions stay on the old one.

Verify:

```sh
kafka-topics --bootstrap-server "$BOOTSTRAP_SERVER" \
  --command-config client.properties \
  --describe --topic "$KAFKA_TOPIC"
```

Record the partition count in your provisioning configuration (Terraform,
cluster API, or an ops note) so later changes are reviewed, not improvised.

## 3. Retention bounds resume and branching

`retention.ms` / `retention.bytes` decide how far back the log reaches. When a
replay asks for a record the broker has already trimmed, kfuse does not error —
`ReadSessionTo` clamps the start to the oldest retained offset and replays only
what survives. A gap in a session's event sequence is not detected, so expired
records translate directly into lost mutations (or into apply errors when the
missing events created a node later events still reference).

What you actually need retained:

- **Resume** needs the newest state image plus every record *after* the offset
  it covers. kfuse checkpoints asynchronously every 100 committed events and
  every 30 seconds while mounted (plus on `kfuse checkpoint` and at branch
  time), so in normal operation only a small tail must be retained. Graceful
  unmount drains in-flight checkpoints but does not force a final one.
- **Branching** to an arbitrary offset needs the newest image at or before that
  offset *plus* all records between that image and the branch point. Replaying
  "the whole history" (no image) needs retention back to the session's first
  record.

Guidance:

- Prefer `retention.ms=-1` (unlimited) on the session topic; records are
  protobuf metadata, not file bytes, so the log stays small relative to the S3
  payload.
- If you bound retention, bound it *comfortably above* the oldest offset you
  intend to branch from — and accept that everything older becomes
  unresumable and unbranchable, by design, not by bug.
- State images cap the replay window for resume, not for arbitrary branches.
  Run `kfuse checkpoint` before pausing a session you care about long-term.

Verify retention and current trim state:

```sh
kafka-configs --bootstrap-server "$BOOTSTRAP_SERVER" \
  --command-config client.properties \
  --entity-type topics --entity-name "$KAFKA_TOPIC" \
  --describe --all | grep -i retention

# per-partition low/high watermarks — start of retained history:
kafka-get-offsets --bootstrap-server "$BOOTSTRAP_SERVER" \
  --command-config client.properties \
  --topic "$KAFKA_TOPIC" --time -2   # earliest offsets still retained
```

## 4. Replication: demo durability vs. real durability

Every append is produced with `acks=all` (`sarama.WaitForAll`): the broker
acknowledges only after *all in-sync replicas* persist the record, and the
returned offset is the commit point kfuse trusts. What `all` actually contains
is a cluster property, not a client one:

- **`replication.factor`** — how many brokers hold each partition. Topic
  auto-creation by kfuse passes `-1`, i.e. the broker's
  `default.replication.factor`. On Confluent Cloud topics default to RF=3; a
  single-node broker defaults to 1.
- **`min.insync.replicas`** — how many of those replicas must acknowledge for
  `acks=all` to succeed.

| Deployment | RF | min.insync.replicas | Meaning of `acks=all` |
|---|---|---|---|
| Local demo (`examples/local`, single broker) | 1 | 1 | One broker wrote it. Disk or volume loss destroys every session history. Acceptable only because the stack is disposable. |
| Durable deployment | ≥3 | 2 | The record survives one broker loss and produces fail if fewer than two replicas are in sync. |

Keep `unclean.leader.election.enable=false` (the default on supported
versions): an unclean election lets an out-of-sync replica lead, which can drop
records that were already acknowledged — the write side of the same silent-loss
problem as retention.

## 5. S3 lifecycle: no expiry, no ad-hoc cleanup

Nothing in kfuse expires or garbage-collects S3 objects, and age is not a safe
deletion criterion:

- **Blobs are content-addressed and shared.** One `blobs/sha256/...` object can
  be referenced by many sessions, many events, and many state images. Deleting
  it by age breaks whichever sessions still reference it — on `Get`, the
  checksum/type errors surface as unreadable file content, not a clean
  "expired".
- **State images are the floor for resume.** Deleting an image pushes replay
  back to older images or to log offset 0 — which Kafka retention may no
  longer reach.
- **`meta/` objects are tiny and load-bearing.** Leases, session records, lower
  indexes, and branch provenance are small JSON documents; there is no savings
  in expiring them, and deleting them breaks locking and lineage.

Bucket lifecycle rules must not touch anything under `S3_PREFIX`. If you need
cost control, delete sessions *as a unit* — a session's records, images, and
then-unreferenced blobs together — after confirming no branch still references
them, and only with tooling that understands those references. Today no such
garbage collector exists in the repo, so treat any deletion under `S3_PREFIX`
as destructive.

Verify:

```sh
aws s3api get-bucket-lifecycle-configuration --bucket "$S3_BUCKET"
# expected: NotFound, or no rule whose prefix overlaps S3_PREFIX
```

Object versioning is optional but harmless — image keys are unique per covered
offset and blobs are immutable by construction.

## 6. Least-privilege credentials

### Supported credential mechanisms

The binary reads **environment variables only** (see `.env.example`); there is
no config file, no credential provider chain, no workload identity:

- Kafka: `KAFKA_SASL_USERNAME` / `KAFKA_SASL_PASSWORD` (SASL PLAINTEXT), both
  or neither; `KAFKA_TLS` (default `true`). Unauthenticated plaintext is for
  the local stack only.
- S3: static `S3_ACCESS_KEY` / `S3_SECRET_KEY` via the AWS SDK static
  credentials provider; `S3_ENDPOINT` + `S3_PATH_STYLE` for S3-compatible
  stores; `S3_REGION`. Instance profiles and IRSA are not consulted.

### Kafka ACLs

The client makes exactly these calls: `CreateTopic` (best-effort at startup),
`Metadata`/`Partitions` (topic describe), `GetOffset` (ListOffsets), `Produce`,
and `Fetch` via a raw partition consumer. **No consumer groups are used** — do
not grant group permissions.

Split provisioning from runtime:

- **Provisioning principal** (operator or IaC, not the runtime): `Create` +
  `Describe` on the topic, used once to create `KAFKA_TOPIC` with the intended
  partition count. Alternatively grant `Create` to the runtime key — kfuse's
  `EnsureTopic` treats "already exists" and "not authorized" as success and
  fails only on a genuinely missing/unreadable topic.
- **Runtime principal** (what goes in `.env`): on resource `Topic:KAFKA_TOPIC`:
  - `Describe` — metadata, partition count, offset lookups;
  - `Write` — appending events;
  - `Read` — replay;
  - `Create` — optional, only if you want kfuse to auto-create a missing topic.

Example (generic `kafka-acls`; Confluent Cloud equivalents exist in its CLI/UI):

```sh
# runtime principal — no Create, no group access
kafka-acls --bootstrap-server "$BOOTSTRAP_SERVER" \
  --command-config client.properties \
  --add --allow-principal "User:kfuse-runtime" \
  --operation Describe --operation Read --operation Write \
  --topic "$KAFKA_TOPIC"

# provisioning principal (one-off topic creation)
kafka-acls --bootstrap-server "$BOOTSTRAP_SERVER" \
  --command-config client.properties \
  --add --allow-principal "User:kfuse-provisioner" \
  --operation Create --operation Describe \
  --topic "$KAFKA_TOPIC"
```

No `DescribeConfigs`, `AlterConfigs`, or cluster-level operations are called —
the topic is described via metadata, and its configs are managed by the
operator, not the client.

### S3 IAM policy

kfuse calls exactly four S3 APIs: `PutObject` (blobs, state images, metadata,
leases), `GetObject` (all reads), `ListObjectsV2` (state-image discovery,
per-lower session index), and `DeleteObject` — **only** on lease locks
(`meta/sessions/{id}.lock`) at unmount. Nothing else is ever deleted.

Runtime policy, scoped to `S3_BUCKET`/`S3_PREFIX` (shown for the default
`kfuse/` prefix):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "KfuseList",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::S3_BUCKET_NAME",
      "Condition": {
        "StringLike": { "s3:prefix": ["kfuse/", "kfuse/*"] }
      }
    },
    {
      "Sid": "KfuseReadWrite",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": "arn:aws:s3:::S3_BUCKET_NAME/kfuse/*"
    },
    {
      "Sid": "KfuseLeaseRelease",
      "Effect": "Allow",
      "Action": "s3:DeleteObject",
      "Resource": "arn:aws:s3:::S3_BUCKET_NAME/kfuse/meta/sessions/*.lock"
    }
  ]
}
```

Two deliberate omissions: `s3:CreateBucket` belongs to provisioning (the
binary never creates the bucket; the local stack's `mc` job or your IaC does),
and `s3:DeleteObject` is narrowly scoped — if you omit it entirely, unmount
still succeeds but the stale lease persists for its ~45 s TTL before another
writer can take the session.

## Security and concurrency boundaries

- **Persistence and branching, not a security sandbox.** kfuse does not
  isolate untrusted code. The demos run privileged containers and disposable
  cloud sandboxes for convenience; neither is a hostile-code isolation
  boundary. Code running inside a kfuse mount — including agent code in the
  examples — needs its own sandboxing, and the Kafka/S3 credentials you give
  kfuse should be scoped to what a compromised mount could reach.
- **The writer lease is best-effort, not an atomic distributed lock.** Mount
  refuses a second healthy writer while a live lease exists, but concurrent
  writers are not strongly fenced: a frozen or crashed writer can miss losing
  the lease until its next renewal, so split-brain around lease expiry is a
  real operational limitation, not a correctness guarantee.
- **Not the sole copy of your data.** kfuse can only resume or branch as far
  back as retained Kafka records and S3 state images allow; Kafka retention
  and bucket lifecycle policy bound recovery, and remote state cleanup is not
  automatic. Keep independent backups of important data and verify your
  retention settings before relying on resume or branch for anything you
  cannot afford to lose.

## Compatibility policy

During the alpha period, kfuse does not guarantee cross-version compatibility
for stored state. Sessions, checkpoint state images, and Kafka event records
written by one release may not be readable by a different release. Resume and
branch within the release that created the session, and treat existing remote
state as disposable across upgrades. Breaking changes to stored formats will
be called out in the release notes; this policy will be revisited before a
stable release.

## Supported filesystem surface

- Linux FUSE and macOS (macFUSE); release builds target `linux/amd64`,
  `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.
- Supported filesystem behavior covers regular files, directories, symlinks,
  rename, truncate, attributes, and fsync. Hardlinks, xattrs, ACLs, advisory
  locks, and device/socket/FIFO files are outside the current scope.
- Branching from an old offset requires the parent records and any needed
  state image to remain available (see section 3). Blob cleanup and deletion
  of retained remote history are not automatic (see section 5).

## Troubleshooting

| Symptom | Next check |
|---|---|
| `missing required env: ...` | Fill `.env` for hosted examples or export the variables required by the CLI. The local demo supplies its own configuration. |
| SASL error `[58]` | Use a Confluent Cloud **Kafka API key**, not a Global/org key. |
| `mount never became live` | Use Linux FUSE support or a privileged Linux container with `/dev/fuse`; on macOS install macFUSE. |
| `session locked` | Another live mount owns the session lease; unmount it or wait for lease expiry. |
| `Device or resource busy` on unmount | Close files and move shells/processes out of the mounted lower before `kfuse umount`. |
| `stage=auth` in launcher output | Kafka SASL or S3 credentials rejected; the launcher prints the next action — see [examples/local/README.md](examples/local/README.md#failure-stages). |
| `stage=broker` in launcher output | Broker unreachable; check `BOOTSTRAP_SERVER`, egress, and `KAFKA_TLS` — see the failure-stages table. |
| `stage=bucket` in launcher output | S3 bucket missing or wrong region/endpoint — see the failure-stages table. |
| `stage=topic` in launcher output | `KAFKA_TOPIC` missing; create it or grant Create/Describe ACLs — see the failure-stages table. |
| `stage=fuse` in launcher output | No usable FUSE on the host — see the failure-stages table. |

## Failure modes this prevents

| Silent symptom | Likely cause |
|---|---|
| Resumed session mounts but shows an empty/stale overlay | Partition count changed, or records before the retained window expired |
| `session: replay ...` or apply errors on mount | Retention trimmed records mid-history; or a referenced blob/image was deleted |
| Old branch point fails, recent ones work | Kafka retention reached the records between the branch offset and its covering image |
| `stage=topic` / "topic not found" at startup | Topic missing and runtime ACL lacks `Create` — intended behavior |
| History lost despite `acks=all` | RF=1 broker (single-node demo) lost its disk — no replication existed to acknowledge into |
