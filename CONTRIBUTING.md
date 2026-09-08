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
5. CI runs `fmt-check`, `vet`, `lint`, `build`, `test`, and a Docker build on
   every PR. Integration tests need repository secrets and do not run for
   forks; a maintainer will run them before merging if the change touches the
   Kafka or S3 paths.

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

Go 1.26.4 or newer (see `go.mod`). The `Makefile` wraps the usual commands:

```sh
make              # fmt-check + vet + lint + build + test
make build        # go build -o kfuse ./cmd/kfuse
make test         # unit tests, no credentials needed
make vet lint fmt  # go vet ./... ; golangci-lint run ./... ; gofmt -w .
make integration-test  # needs a repo-root .env (see below)
make probe        # read-side FUSE probe in Docker, no Kafka or S3
make docker-build # build the release Dockerfile locally
make release-snapshot  # goreleaser --snapshot: archives + checksums in dist/
```

CI (`.github/workflows/ci.yml`) runs `fmt-check`, `vet`, `lint`, `build`,
`test`, and a `docker build` smoke test on every pull request. Integration
tests only run on pushes to `main` and manual dispatch, and are skipped unless
the repository secrets below are set.

## Releases

Maintainers cut a release by pushing a tag: `git tag v0.x.y && git push origin
v0.x.y`. `.github/workflows/release.yml` runs GoReleaser, which builds
`linux/amd64` and `linux/arm64` binaries, attaches tar.gz archives and
`checksums.txt` to the GitHub Release, and pushes a multi-arch image to
`docker.io/addisonhuddy/kfuse`. The workflow needs `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` repository secrets.

### Integration tests

Integration tests are behind `//go:build integration`. They read the repo-root
`.env` (copy `.env.example`) — `.env` is the only credentials file kfuse looks
for, and it is gitignored. Never commit credentials under any other name.

Required values (the same names CI takes from repository secrets):

| Variable | Where it comes from |
| --- | --- |
| `KF_KAFKA_BROKERS` | Confluent Cloud cluster → Cluster settings → bootstrap server (`pkc-….confluent.cloud:9092`) |
| `KF_KAFKA_SASL_USERNAME` / `KF_KAFKA_SASL_PASSWORD` | Confluent Cloud **cluster-level** Kafka API key (Cluster → API keys). A Global/org key fails SASL with `[58]` |
| `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | An IAM user or role with read/write on the bucket prefix below |
| `KF_BLOB_BUCKET` | An S3 bucket you own; tests write under the `kfuse/test/…` prefix and need `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, and `s3:ListBucket` on it |

`KF_KAFKA_TLS=true` and `KF_KAFKA_TOPIC=kfuse.events` are the defaults the tests
and demos assume; the topic is created with 8 partitions if it does not exist.

## Demos

```sh
./examples/functional-demo/run.sh --probe    # FUSE only (= make probe)
./examples/functional-demo/run.sh            # full walkthrough
./examples/best-of-n/run.sh
```

`--probe` is the cheap check. The other two need hosted Kafka and S3.
