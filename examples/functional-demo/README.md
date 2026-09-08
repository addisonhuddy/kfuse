# functional-demo

Walks a kfuse session through create, write, unlink, dirs, symlinks, attrs,
rename, sparse CoW, resume, branch, and checkpoint. Runs in Docker against
hosted Confluent Cloud + AWS S3.

```sh
./examples/functional-demo/run.sh               # needs Docker + .env
./examples/functional-demo/run.sh --probe       # FUSE checks, no Kafka
./examples/functional-demo/run.sh --shell       # live mount + bash
./examples/functional-demo/run.sh --cross-host  # two sandboxes, one session
```

`run.sh` builds the image and runs the walkthrough. It exits non-zero on the
first failing step and prints `DEMO PASS (42/42 steps)` when everything works.

## Credentials

The repo-root `.env` loads automatically. Pass another path with
`--creds <file>`. Already-exported environment values win.

Short names and canonical names both work:

```
BOOTSTRAP_SERVER=pkc-<cluster>.us-west-2.aws.confluent.cloud:9092   # = KF_KAFKA_BROKERS
CONFLUENT_CLOUD_KEY=...                                            # = KF_KAFKA_SASL_USERNAME
CONFLUENT_CLOUD_SECRET=...                                         # = KF_KAFKA_SASL_PASSWORD
REGION=us-west-2                                                   # = AWS_REGION
AWS_ACCESS_KEY=...                                                 # = AWS_ACCESS_KEY_ID
AWS_SECRET_KEY=...                                                 # = AWS_SECRET_ACCESS_KEY
BUCKET=kfuse-demo                                                  # = KF_BLOB_BUCKET
```

Use a Confluent **Kafka API key** (Cluster → API keys), not a Global/org key.

## Run the image directly

```sh
docker build -f examples/functional-demo/Dockerfile -t kfuse-functional-demo .
docker run --rm --privileged --device /dev/fuse \
  --env-file <creds.env> kfuse-functional-demo          # also: probe | shell | bash
```

`--env-file` needs the canonical names (`KF_*`, `AWS_*`). FUSE needs
`--privileged --device /dev/fuse`. The topic `kfuse.events` must exist.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `missing required env: ...` | copy `.env.example` → `.env` and fill it in, or pass `--creds` |
| `mount never became live` | `--privileged` + `/dev/fuse` |
| SASL error `[58]` | Kafka API key, not a Global key |
| topic errors on first mount | create `kfuse.events` (8 partitions) |
