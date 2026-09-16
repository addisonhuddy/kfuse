---
name: testing-installer
description: Runtime-test kfuse release and source installers, including private-repository asset staging and checksum rejection.
---

# Installer runtime testing

Run from the kfuse checkout on Linux. These tests need no Kafka, S3, FUSE mount, or desktop. Save shell transcripts; do not record an idle desktop.

## Devin Secrets Needed

- None for source mode, local fake-release tests, or public releases.
- For a private repository, authenticated `gh` with repository contents/releases read access is required (typically supplied through `GH_TOKEN`). Verify with a small `gh api` request; avoid printing tokens or full auth configuration.

## Setup

Use the Go version required by go.mod. Have Bash, curl, tar, Python 3, sha256sum, and Perl shasum available. Review the current install.sh and scripts/install_test.sh before selecting flags or expected messages.

Unauthenticated raw GitHub/release URLs can return 404 for private repositories; the latest-release API can also be rate-limited. Do not misclassify these as installer failures.

If approved to mirror private assets:

1. Fetch the branch script with `gh api 'repos/addisonhuddy/kfuse/contents/install.sh?ref=BRANCH' -H 'Accept: application/vnd.github.raw'`; compare it to the local checkout.
2. Stage an actual release with `gh release download TAG -R addisonhuddy/kfuse -D TEMP/server/download/TAG`.
3. Save actual latest release metadata containing `tag_name` to TEMP/server/api/latest.
4. Serve TEMP/server using `python3 -m http.server PORT --bind 127.0.0.1 --directory TEMP/server`.
5. Set KFUSE_RELEASE_BASE_URL to `http://127.0.0.1:PORT/download` and KFUSE_RELEASE_API_URL to `http://127.0.0.1:PORT/api/latest` for installer invocations. Stop the server afterward.

Explicitly disclose this transport substitution: it verifies installation of real released assets, not the public one-liner.

## Focused runtime coverage

- Pipe authenticated or public script retrieval into Bash with a temporary --dest. Verify executable mode, `version`, and `--help`. Released version output may omit the tag's leading v.
- Verify explicit version and local script piping produce the identical binary.
- Test source execution from both checkout and another cwd. File-based invocation locates its checkout via BASH_SOURCE; piped source can use the current checkout, but must reject outside a checkout.
- Use a separate mirrored fixture with a zeroed checksum; assert refusal, absent new binary, and unchanged hash of any prior installation.
- Use a minimal PATH containing shasum but not sha256sum or go to prove release mode's fallback and lack of Go dependency. Include gzip because tar may invoke it.
- Test missing versions, unknown flags, and unsupported OS with exit codes and no installed binary. Run `make test-install` for its local HTTP fixture cases; simulated Darwin selection is not native Darwin runtime coverage.
- Do not claim macOS binary execution, FUSE mounting, default destination permissions, or public GitHub downloading unless actually tested.
