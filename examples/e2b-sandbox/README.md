# E2B sandbox

This example drives disposable [E2B](https://e2b.dev/docs) sandboxes from a
Python control process while kfuse stores the workspace in Kafka and S3. The
scripted demo writes a large activity file, checkpoints it to S3, removes the
origin sandbox, and verifies that two branches can independently append to
the same persisted file before replaying the checkpoint and a branch's latest
state.

The demo drives a `PersistentSandbox` controller that recreates the sandbox
and remounts the session when the current sandbox stops. `uv run main.py
--repl` exposes the same controller as an interactive prompt (`:restart`
moves the session to a fresh sandbox).

## Credentials

`uv run` reads the repo-root `.env` when it exists. Canonical names are:

```
KF_KAFKA_BROKERS=...
KF_KAFKA_SASL_USERNAME=...
KF_KAFKA_SASL_PASSWORD=...
AWS_REGION=...
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
KF_BLOB_BUCKET=...
E2B_API_KEY=...
```

The short names accepted by the other examples are also supported:
`BOOTSTRAP_SERVER`, `CONFLUENT_CLOUD_KEY`, `CONFLUENT_CLOUD_SECRET`, `REGION`,
`AWS_ACCESS_KEY`, `AWS_SECRET_KEY`, and `BUCKET`. A Confluent Kafka API key is
required, rather than an organization key. `E2B_API_KEY` is used only by the
control process and is never sent into a sandbox.

## Run

From this directory:

```sh
uv run main.py
uv run main.py --repl
```

Each run generates one unique `KF_BLOB_PREFIX`, shared by the sandboxes in
that run. The Linux kfuse binary is uploaded to each default E2B sandbox at
runtime, and the lower directory is prepared before the first mount. The
default E2B base template already includes FUSE support, so no custom template
is required.

## Session handoff

A mount holds a best-effort S3 lease with a 45-second TTL; it refuses a second
mount while held and drops a writer to read-only once its heartbeat sees the
lease taken, but it is not strict fencing (see the README's Limits). `kfuse
umount` blocks until the daemon exits and releases the lease, so killing the
old sandbox afterward is safe. Never mount one session in two sandboxes
concurrently; if a sandbox dies mid-mount, wait for the TTL to lapse (or
checkpoint and branch) before mounting elsewhere. The scripted flow
checkpoints before unmounting the first sandbox and remounting in the second.

`kfuse checkpoint` flushes a state image to S3 and prints the covered Kafka
offset. The scripted demo passes that offset as `--to` when creating each
child, appends different lines in parallel, and replays both the checkpoint
and one child's latest committed state.
All sandboxes use the stable `KF_LOWER_ID=e2b-demo`, so sessions can resolve
their shared lower directory across disposable sandboxes.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `missing required environment variables` | Fill the canonical or short names above, including `E2B_API_KEY`. |
| SASL error `[58]` | Use a Confluent Kafka API key, not an organization key. |
| `kfuse mount ... child failed to start` with `session locked: held by <host>` | The examples retry resumed mounts after waiting 47 seconds for the old writer lease. `kfuse umount` waits for the daemon to exit and release the lease before returning. |
| `umount: daemon pid <pid> still running after 30s` | Something (a shell or process cwd, an open file) is inside `/home/user/work`, so `fusermount` gets `Device or resource busy`. Run `kfuse umount` from outside the mountpoint; the examples always do. |
| `kfuse mount` cannot connect to Kafka or S3 | Confirm the broker, region, bucket, and credentials are reachable from the E2B sandbox, and that the broker is the Kafka endpoint including its port. |
