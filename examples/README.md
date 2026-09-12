# kfuse examples

Two demos show how a kfuse workspace persists, resumes, and branches. Run the
commands below from the repository root unless noted otherwise. These examples
are for evaluating alpha software: the privileged containers and disposable
sandboxes are not a security boundary for untrusted code — see
[Status](../README.md#status-public-alpha) in the root README.

| Demo | What it shows | Successful output |
|---|---|---|
| [Local](local/README.md) | File operations, persistence, checkpoints, and branching with Apache Kafka and MinIO | `COMPLETE (42/42)` |
| [E2B](e2b-sandbox/README.md) | Independent branches and checkpoint replay across disposable cloud sandboxes | Branch summary followed by `COMPLETE` |

## Local: no cloud credentials

Requires a Linux Docker host with Compose and `/dev/fuse`. Go is built inside
the demo image. The launcher supplies dummy development credentials.

```sh
make local-demo
./examples/local/demo.sh --shell
make local-down
```

The scripted and interactive modes are alternatives; type `exit` to leave the
interactive mount before stopping the stack. Stopping preserves data volumes.
See the [local guide](local/README.md) for other modes and hosted storage options.

## E2B: cloud sandboxes

Requires Go 1.26.4 or newer, uv, Python 3.10 or newer, and E2B/Kafka/S3 credentials.
Configure the repository-root `.env` using [the template](../.env.example),
including `E2B_KEY`. The launcher loads that file; non-empty exported variables
win. The E2B command-line entry point does not accept `--creds`.

```sh
cd examples/e2b-sandbox
uv run main.py
```

Use `uv run main.py --repl` for an interactive workspace and `:restart` to move
it to a fresh sandbox. No local Docker or FUSE is needed. The demo creates
billable resources and retains remote history after closing its sandboxes.
See the [E2B guide](e2b-sandbox/README.md) for setup and session handoff details.
