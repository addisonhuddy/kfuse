# kfuse examples

kfuse is a branching overlay filesystem for coding agents: a FUSE mount over a
base directory where every mutation is committed to Kafka and S3, so a session
can be paused, resumed, branched, and reverted across hosts.

It runs in any Linux sandbox with `/dev/fuse`. The demos use Docker.

Every example loads the repo-root `.env` automatically; pass
`--creds <file>` to use another file. Already-exported environment values win.
Both canonical names and the short aliases (`BOOTSTRAP_SERVER`,
`CONFLUENT_CLOUD_KEY`, `CONFLUENT_CLOUD_SECRET`, `REGION`, `AWS_ACCESS_KEY`,
`AWS_SECRET_KEY`, `BUCKET`) are accepted.

| Demo | What it shows | Pass line |
|---|---|---|
| [`functional-demo`](functional-demo/README.md) | POSIX walkthrough: create, write, dirs, symlinks, attrs, rename, sparse CoW, resume, branch, checkpoint | `DEMO PASS (42/42 steps)` |
| [`best-of-n`](best-of-n/README.md) | Dirty workspace → checkpoint → 3 parallel hypothesis sandboxes → judge | `BEST-OF-N PASS` |
| [`e2b-sandbox`](e2b-sandbox/README.md) | Durable kfuse sessions across disposable E2B sandboxes and parallel branches | `E2B DEMO PASS` / `E2B BRANCH DEMO PASS` |

```sh
./examples/functional-demo/run.sh                # needs creds; default .env
./examples/functional-demo/run.sh --probe        # FUSE only, no Kafka
./examples/functional-demo/run.sh --shell        # interactive mount
./examples/functional-demo/run.sh --cross-host   # two sandboxes, one session
./examples/best-of-n/run.sh
./examples/best-of-n/run.sh --creds ~/secrets/my-kfuse.env
cd examples/e2b-sandbox && uv run main.py
```
