# best-of-n

A coding agent's workspace is more than a git tree: checkout plus generated
files, failed builds, test databases, half-written notes. Git snapshots source;
it can't fork that dirty POSIX tree. kfuse can.

This demo reproduces a bug in a dirty workspace, checkpoints, forks three
hypothesis sandboxes in parallel, scores them, and keeps the parent untouched.

## The story

A config parser (`parse.sh`) leaks comment lines into its key list. `test.sh`
fails until that's fixed.

1. **Parent** — creates a session, runs `test.sh` (red), leaves a `repro.log`
   note, `kfuse checkpoint`s, then branches three children from that offset.
2. **Three hypotheses, in parallel** — each mounts its own child session
   (separate writer leases) and tries a one-line patch:

   | Hyp | Patch | Result |
   |---|---|---|
   | A | `grep -v '^#'` — skip column-0 comments | **fail** — indented comments still leak |
   | B | `grep -v '^[[:space:]]*$'` — drop blank lines | **fail** — fixes blanks, not comments |
   | C | `grep -vE '^[[:space:]]*(#\|$)'` — skip blanks + comments | **pass** |

3. **Judge** — remounts the parent: `test.sh` still fails, `repro.log` is
   intact, no winner files leaked in. `kfuse session ls` lists parent and children.

```sh
./examples/best-of-n/run.sh
./examples/best-of-n/run.sh --creds ~/secrets/my-kfuse.env
```

Prints `BEST-OF-N PASS` when A fails, B fails, C passes, and the parent is
untouched.

## Credentials

Same as `functional-demo`: hosted Confluent Cloud + AWS S3. The repo-root
`.env` loads automatically; pass another file with `--creds <file>`.
Already-exported environment values win. Short names (`BOOTSTRAP_SERVER`,
`CONFLUENT_CLOUD_KEY`, `CONFLUENT_CLOUD_SECRET`, `REGION`, `AWS_ACCESS_KEY`,
`AWS_SECRET_KEY`, `BUCKET`) and canonical names (`KF_*`, `AWS_*`) both work.

Use a Confluent **Kafka API key**, not a Global/org key.
