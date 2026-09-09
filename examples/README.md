# kfuse examples

kfuse is a branching overlay filesystem for coding agents: a FUSE mount over a
base directory where every mutation is committed to Kafka and S3, so a session
can be paused, resumed, branched, and reverted across hosts.

It runs in any Linux sandbox with `/dev/fuse`. The demos use Docker.

Every example loads the repo-root `.env` automatically; pass
`--creds <file>` to use another file. Already-exported environment values win.
Use `BOOTSTRAP_SERVER`, `KAFKA_SASL_USERNAME`, `KAFKA_SASL_PASSWORD`,
`S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, and `S3_BUCKET`.

| Demo | What it shows | Pass line |
|---|---|---|
| [`local`](local/README.md) | POSIX walkthrough against local Apache Kafka KRaft + MinIO; also runs against hosted creds via `run.sh --creds` | `DEMO PASS (42/42 steps)` |
| [`e2b-sandbox`](e2b-sandbox/README.md) | Durable kfuse sessions across disposable E2B sandboxes and parallel branches | `E2B DEMO PASS` / `E2B BRANCH DEMO PASS` |

```sh
make local-demo
./examples/local/run.sh --probe
./examples/local/run.sh --shell
./examples/local/run.sh --cross-host --creds .env
cd examples/e2b-sandbox && uv run main.py
```
