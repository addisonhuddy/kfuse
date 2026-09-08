---
name: testing-e2b-examples
description: Run and runtime-test the E2B (and similar disposable-sandbox) kfuse examples in examples/e2b-sandbox against real Confluent Cloud + S3, including the sandbox-death recovery paths.
---

# Testing the E2B kfuse examples

Scripts live in `examples/e2b-sandbox` and are driven with `uv run` from that
directory. They build a static Linux `kfuse` binary via `go build` (needs the Go
toolchain on PATH) and upload it into each sandbox, so no custom E2B template is
required.

## Credentials

`kfuse_e2b.load_env()` accepts canonical `KF_*`/`AWS_*` names or the short
aliases `BOOTSTRAP_SERVER`, `CONFLUENT_CLOUD_KEY`, `CONFLUENT_CLOUD_SECRET`,
`REGION`, `AWS_ACCESS_KEY`, `AWS_SECRET_KEY`, `BUCKET`, plus `E2B_API_KEY`.
All of these are stored secrets, so the scripts run with no manual mapping. A
name the scripts do not recognise surfaces as
`missing required environment variables: KF_KAFKA_BROKERS` (or the equivalent
for the other variables) rather than as a connection failure.

## Recording in a GUI terminal

There is no web UI, so acceptance evidence is a maximized terminal. On this box
only `konsole` is installed and `DISPLAY=:0`.

- A `konsole` started without `--separate` can be adopted by an existing Konsole
  process and will NOT inherit the env you exported — verify inside the window
  with a `[ -n "$VAR" ] && echo set` loop (never echo values) before running.
- Maximize with `wmctrl -i -r <win> -b add,maximized_vert,maximized_horz`.
- `ctrl+plus` zooms the font; `ctrl+shift+plus` types `+` into the shell instead.
- Do not pipe script output through `tail -N` for evidence: the interesting
  recovery lines are near the top and get cut. Use `grep -E` for the specific
  lines, or capture full output.

## Timings (do not mistake waiting for hanging)

- Sandbox create + provision + mount: ~6-15s each. The branching `main.py`
  demo (5 sandboxes, 12 MB file) takes ~60-90s total; abrupt-kill recovery
  runs ~60-110s.
- `PersistentSandbox` lives in `main.py` and defaults to `timeout=600`.
  Read the current constructor before relying on an idle timeout; directly
  killing the sandbox exercises recovery faster and more deterministically.
- The recovery branch that waits out the S3 writer lease sleeps
  `LEASE_RETRY_DELAY = 47`s, so a recovery can legitimately take ~70s.

## Exercising the lease-contention recovery path deterministically

Idling until E2B reaps the sandbox often does NOT produce the
`previous sandbox lease is still active; waiting 47s before retrying` line,
because the lease has already expired by then. To force it, kill the sandbox
abruptly from the control process while the lease is fresh, using a throwaway
harness (delete it afterwards):

```python
from rich.console import Console
from kfuse_e2b import blob_prefix, build_kfuse_binary, load_env, sandbox_env
from main import PersistentSandbox

creds = load_env()
c = PersistentSandbox(sandbox_env(creds, blob_prefix()),
                      build_kfuse_binary(), creds["E2B_API_KEY"], Console())
try:
    c.execute("printf 'lease survives\\n' > lease.txt")
    c.checkpoint()
    sid = c.session_id
    c.sandbox.kill()                  # unclean death, lease still held
    recovered = c.execute("cat lease.txt")
    assert c.session_id == sid
    assert recovered == "lease survives\n"
finally:
    c.close()
```

Assert the session id is byte-identical before and after, not merely that a
mount succeeded — a fresh session would also "mount successfully".

## Distinguish checkpoint recovery from Kafka replay

- The stock branching demo cleanly unmounts before killing writer sandboxes;
  it does not itself exercise abrupt death.
- To prove replay beyond an S3 checkpoint, checkpoint an empty session, then
  create/append/overwrite/rename/delete a few files, explicitly flush/fsync
  their writes, and kill without another checkpoint or unmount. Stay below
  the automatic event threshold and materializer interval (currently 100
  events and 30 seconds; recheck source if these change). Assert exact bytes,
  line order, deleted/old names absent, same session ID, and a different
  sandbox ID after recovery. Checkpoint afterward and require an offset
  greater than the base offset.
- Earlier runs observed apparent lost writes with immediate uncheckpointed
  death. Do not accept that as intended behavior: Write is documented as a
  synchronous durable commit. Use flush/fsync and record timing and sequence
  count to distinguish client buffering from a durability regression.
- Checkpoint output exposes the covered Kafka offset, not each individual
  Append call. Do not describe it as a direct trace of all Append returns.
- Verify branch provenance using `go version -m .build/kfuse` (revision,
  CGO_ENABLED=0, GOOS=linux, GOARCH=amd64) and compare local SHA256 with
  `/usr/local/bin/kfuse` inside the sandbox.
- Daemon logs are under `$KF_STATE_DIR/$KF_LOWER_ID/daemon.log`. Capture
  before killing the sandbox; a failed initial resume may log a lease lock,
  followed by a successful retry after 47 seconds.

## Leak check

Always finish with, and include the output of:

```sh
uv run python -c "from e2b import Sandbox; print(Sandbox.list().next_items())"
```

`Sandbox.list()` returns a paginator — it is not iterable; call `next_items()`.

## Devin Secrets Needed

`E2B_API_KEY`, `BOOTSTRAP_SERVER`, `CONFLUENT_CLOUD_KEY`,
`CONFLUENT_CLOUD_SECRET`, `AWS_ACCESS_KEY`, `AWS_SECRET_KEY`, `REGION`, `BUCKET`.
A Confluent *Kafka* API key is required (an org key fails with SASL error 58).
