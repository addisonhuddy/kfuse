# Contributing

Thanks for your interest in kfuse. Bug reports, docs fixes, and small patches
are welcome as pull requests. For anything large (new subsystems, wire-format
or on-disk changes, dependency swaps) please open an issue first so we can
agree on the design before you spend time on it.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Security issues go through [SECURITY.md](SECURITY.md), not the issue tracker.

## Workflow

1. Fork the repo and clone your fork.
2. Create a branch: `git checkout -b my-change`.
3. Make the change, add or update tests, and run `make` (see below) until it
   is green.
4. Push the branch to your fork and open a pull request against `main`. Fill
   in the PR template; link the issue if there is one.
5. CI runs on every PR, including forks — see "CI" below. The hosted
   integration tests never run on pull requests; a maintainer will trigger
   them when the change touches the Kafka or S3 paths.

Keep PRs focused. Squash fixups before asking for review.

## Licensing of contributions

kfuse is licensed under [Apache-2.0](LICENSE). By submitting a pull request you
agree that your contribution is licensed under the same terms. There is no CLA
or DCO sign-off requirement. New Go files should carry the standard header:

```go
// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0
```

## Build and test

Contributor development happens on Linux. You need:

- Go at the version in the `go` directive of `go.mod` or newer;
- `golangci-lint` v2 for `make lint` (CI pins the version in
  `.github/workflows/ci.yml`);
- `fuse3`/`fusermount3` and a working `/dev/fuse` for `make test-fuse` and
  for running `kfuse mount` yourself;
- Docker with the Compose v2 plugin (`docker compose`) for the `make local-*`
  targets, `make test-local-network`, and the container demos.

The `Makefile` wraps the usual commands:

```sh
make              # fmt-check + vet + lint + build + test — the pre-push gate
make fmt-check    # fail if any file needs gofmt
make vet lint fmt # go vet ./... ; golangci-lint run ./... ; gofmt -w .
make build        # go build -o kfuse ./cmd/kfuse
make test         # unit tests, no credentials needed
make test-race    # unit tests under the race detector
make test-fuse    # real FUSE mount tests; needs Linux /dev/fuse + fusermount3
make test-install # installer tests, no network or credentials needed
make test-preflight      # preflight-script tests against a fake docker CLI
make test-local-network  # checks the local stack's loopback/private-network
                         # wiring; needs docker + compose and python3 or jq
make integration-test  # needs a repo-root .env (see below); the same suite
                       # also runs against the credential-free local stack
make local-up local-demo local-down  # local Kafka (KRaft) + MinIO stack and demo
make docker-build # build the release Dockerfile locally
make release-snapshot  # goreleaser --snapshot: archives, SBOMs (needs syft), checksums in dist/
```

### Platform support

kfuse mounts are Linux-only, and a native build is too: `internal/fs` uses
Linux-only rename flags (`unix.RENAME_EXCHANGE`, `unix.RENAME_NOREPLACE`), so
`GOOS=darwin go build ./...` does not compile. Cross-compiling *to* Linux is
supported — `GOOS=linux GOARCH=amd64 ./install.sh --output dist/kfuse` on any
host produces a binary for a Linux sandbox (likewise `GOARCH=arm64`). This is
how the E2B demo builds its sandbox binary; it does not mean macOS can host a
mount. Releases ship `linux/amd64` and `linux/arm64` binaries only.

### CI

CI (`.github/workflows/ci.yml`) runs on every pull request — including
forks — and on pushes to `main`:

- `unit`: `fmt-check`, `vet`, `test-install`, `test-preflight`,
  `golangci-lint`, `build`, `test`, `test-race`;
- `fuse`: installs `fuse3` and runs `make test-fuse` — a missing `/dev/fuse`
  or `fusermount3` fails the job rather than skipping it;
- `docker`: a `docker build` smoke test plus a GoReleaser `--snapshot` dry
  run;
- `local-integration`: starts the `examples/local` Kafka + MinIO stack
  (`./examples/local/up.sh`), then runs `make test-local-network` and
  `make integration-test` against it. It needs no repository secrets, so it
  runs on fork PRs too.

The hosted `integration` job runs `make integration-test` against Confluent
Cloud and AWS S3 using repository secrets. It runs only on pushes to `main`
and manual `workflow_dispatch` — never on pull requests — and skips cleanly
when the secrets are not configured.

## Releases

Maintainers cut a release by pushing a tag: `git tag v0.x.y && git push origin
v0.x.y`. `.github/workflows/release.yml` runs GoReleaser, which builds
`linux/amd64` and `linux/arm64` binaries, attaches tar.gz archives and
`checksums.txt` to the GitHub Release, and pushes a multi-arch image to
`docker.io/addisonhuddy/kfuse`. The workflow needs `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` repository secrets. Maintainer-side release and repository
protections are documented in [RELEASING.md](RELEASING.md).

### Integration tests

Integration tests are behind `//go:build integration`. They read the repo-root
`.env` (copy `.env.example`) or already-exported environment variables; exported
values win. `.env` is gitignored. Never commit credentials under any other name.

Required values (the same names CI takes from repository secrets):

| Variable | Where it comes from |
| --- | --- |
| `BOOTSTRAP_SERVER` | A Kafka bootstrap address; for Confluent Cloud, Cluster settings → bootstrap server (`pkc-….confluent.cloud:9092`) |
| `KAFKA_SASL_USERNAME` / `KAFKA_SASL_PASSWORD` | Optional as a pair; for Confluent Cloud use a **cluster-level** Kafka API key (Cluster → API keys). A Global/org key fails SASL with `[58]` |
| `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET` | An S3-compatible bucket and credentials; tests write under a `kfuse/test/…` prefix |
| `S3_REGION` | Optional region override (`us-east-1` is the default) |
| `S3_ENDPOINT`, `S3_PATH_STYLE` | Optional for MinIO or another S3-compatible endpoint |

`KAFKA_TLS=true` and `KAFKA_TOPIC=kfuse.events` are the defaults the hosted
tests and demos assume; the topic is created with 8 partitions if it does not
exist. For a credential-free local run, `make local-up` starts Apache Kafka in
single-node KRaft mode plus MinIO; `set -a; . examples/local/local.env; set +a`
loads the local values, then `make integration-test` runs the same suite CI's
`local-integration` job runs.

## Demos

Run the credential-free local walkthrough on a Linux Docker host:

```sh
make local-demo
make local-down
```

For cloud sandbox persistence and branching, configure E2B, Kafka, and S3
credentials in the repository-root `.env`, then run:

```sh
cd examples/e2b-sandbox && uv run main.py
```

See the [local](examples/local/README.md) and
[E2B](examples/e2b-sandbox/README.md) guides for prerequisites and interactive
modes.
