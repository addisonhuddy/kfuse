# functional-demo

Walks a kfuse session through create, write, unlink, dirs, symlinks, attrs,
rename, sparse CoW, resume, branch, and checkpoint. Runs in Docker against
hosted Confluent Cloud + AWS S3 by default, or the local Apache Kafka + MinIO
stack with `--local`.

```sh
./examples/functional-demo/run.sh               # needs Docker + .env
./examples/functional-demo/run.sh --probe       # FUSE + S3 checks, no Kafka
./examples/functional-demo/run.sh --shell       # live mount + bash
./examples/functional-demo/run.sh --cross-host  # two sandboxes, one session
./examples/functional-demo/run.sh --local --creds examples/local/container.env
```

`run.sh` builds the image and runs the walkthrough. It exits non-zero on the
first failing step and prints `DEMO PASS (42/42 steps)` when everything works.

## Credentials

The repo-root `.env` loads automatically. Pass another path with
`--creds <file>`. Already-exported environment values win.

Use the canonical external-service names:

```
BOOTSTRAP_SERVER=pkc-YOUR_CLUSTER.us-west-2.aws.confluent.cloud:9092
KAFKA_SASL_USERNAME=...
KAFKA_SASL_PASSWORD=...
S3_REGION=us-west-2
S3_ACCESS_KEY=...
S3_SECRET_KEY=...
S3_BUCKET=kfuse-demo
```

Use a Confluent **Kafka API key** (Cluster → API keys), not a Global/org key.
For the local stack, run `make local-up` first and use `--local` with
`examples/local/container.env` so demo containers reach `host.docker.internal`.

## Run the image directly

```sh
docker build -f examples/functional-demo/Dockerfile -t kfuse-functional-demo .
docker run --rm --privileged --device /dev/fuse \
  --env-file <creds.env> kfuse-functional-demo          # also: probe | shell | bash
```

`--env-file` uses the same names as `.env.example`. FUSE needs
`--privileged --device /dev/fuse`. `kfuse session new` creates the configured
topic when it is absent.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `missing required env: ...` | copy `.env.example` → `.env` and fill it in, or pass `--creds` |
| `mount never became live` | `--privileged` + `/dev/fuse` |
| SASL error `[58]` | Kafka API key, not a Global key |
| topic errors on first mount | check broker connectivity; `session new` creates `kfuse.events` when allowed |
