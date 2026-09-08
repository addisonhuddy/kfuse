# perf — kfuse benchmark suite

Repeatable, adhoc benchmarks for kfuse (issue #35): run before/after a change
to check nothing added a bottleneck. Captures:

| metric | scenario | source |
|---|---|---|
| write latency p50/p95/p99, split into blob PUT / Kafka append / apply | `write4k`, `write64k` | client `client_write` + daemon `fuse_write`, `blob_put`, `commit.write.kafka_append`, `commit.write.apply` |
| read latency, warm vs cold, overlay (blob-backed) vs lower | `read` | client `client_read{cold-overlay,...}` + daemon `fuse_read`, `blob_get` |
| metadata ops (stat/readdir/lookup), wide vs deep, cold vs warm | `meta` | client `client_stat`/`client_readdir` + daemon `fuse_lookup`/`fuse_getattr`/`fuse_readdir` |
| commit throughput vs concurrent writers | `throughput` | `throughput` summaries (events/s for 1/2/4/8 writers) |
| checkpoint cost (marshal/upload/image size) + impact on in-flight writes | `checkpoint`, `ckpt-impact` | daemon `checkpoint` events + `client_write` during an upload |
| branch (fork) time | `branch` | daemon `branch` events + wall time |
| resume/mount time, with vs without a state image | `resume` | daemon `resume` events (image_load/log_read/apply, replayed_events) + wall time |
| daemon memory footprint vs overlay size | `mem` | `mem` summaries (VmRSS) |

## How it works

The daemon side is instrumented via `internal/perf`: when `KF_PERF_LOG`
names a file, every instrumented phase appends one JSONL event. Unset (the
default), the instrumentation is a single atomic load — production behaviour
is unchanged.

- `loadgen/` — drives file ops against a live mount, emitting client-side
  (full FUSE round trip) JSONL samples.
- `run.sh` — orchestrates the scenarios: builds non-race binaries, makes a
  lower fixture (wide/deep trees, 1 MiB file), and mounts a fresh session per
  scenario with its own `KF_PERF_LOG`.
- `analyze/` — reduces all JSONL to `results.json` (machine-readable, diff it
  across PRs) and `results.md` (percentile tables in µs).

## Running

Against the credential-free local stack (repeatable, recommended):

```sh
./examples/perf/local-stack.sh up      # Apache Kafka KRaft + MinIO-as-S3 (docker)
source examples/perf/.stack-env
./examples/perf/run.sh                 # ~3 minutes
./examples/perf/local-stack.sh down
```

Against real Confluent + S3 (network-realistic numbers), the repo-root `.env`
loads automatically, or pass another file with `--creds <file>`, then run
`run.sh`. Already-exported environment values win, so `source .stack-env` still
works. Use `BOOTSTRAP_SERVER`, `KAFKA_SASL_USERNAME`,
`KAFKA_SASL_PASSWORD`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, and
`S3_BUCKET`.

Results land in `examples/perf/results/<utc-timestamp>/`:

```
results.json    machine-readable percentiles + raw summaries
results.md      human-readable tables
run-info.txt    commit, date, host, go version, brokers
raw/*.jsonl     every sample, per scenario ({scenario}-client / {scenario}-daemon)
```

Baselines are committed under `results/` (just `results.json`, `results.md`,
and `run-info.txt` — raw JSONL stays local) — compare a fresh run's
`results.json` against the newest baseline to spot regressions. Numbers from
the local stack measure kfuse's own overhead (loopback network); absolute
latencies against real Kafka/S3 will be dominated by network RTT.

## Notes

- Requires FUSE on the host (`/dev/fuse`, `fusermount3`) and docker for the
  local stack. No source changes or credentials needed.
- Use non-race builds (run.sh builds its own); `-race` numbers are not
  comparable.
- "cold" reads/metadata come from a freshly-created mount (empty kernel
  caches); "warm" is an immediate second pass.
