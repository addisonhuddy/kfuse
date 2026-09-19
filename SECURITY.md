# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems.

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/addisonhuddy/kfuse/security/advisories/new)
or by email to addisonhuddy@gmail.com. Include the affected version or commit,
a description of the issue, and reproduction steps if you have them.

Response targets (this is a single-maintainer alpha, so these are goals, not
an SLA):

- acknowledgement within 3 business days;
- triage and severity assessment within 7 days;
- a fix or documented mitigation for confirmed high/critical issues within 30
  days, coordinated with the reporter before public disclosure.

Once a fix is ready we will publish a patched release and a GitHub Security
Advisory crediting the reporter unless you prefer to stay anonymous.

## Scope

kfuse mounts a FUSE filesystem and talks to Kafka and S3 using credentials from
the environment. Issues of particular interest:

- privilege escalation or escape from the mount into the host
- credential leakage (to logs, Kafka, S3, or the state directory)
- data corruption or loss in the commit or checkpoint path
- bypass of single-writer fencing (S3 lease) beyond the best-effort behavior
  documented in the README

kfuse is a public alpha: limitations already documented there — such as the
non-atomic S3 lease and the fact that privileged demo containers are not an
isolation boundary — are known constraints rather than vulnerabilities.

## Verifying releases

Release archives on GitHub Releases ship with a `checksums.txt` (SHA-256).
Releases after `v0.1.0` also include an SPDX SBOM per archive, and each
artifact carries a Sigstore-signed build provenance attestation produced by the
release workflow. `install.sh --release`
verifies the SHA-256 only; to verify the provenance yourself:

```sh
gh attestation verify kfuse_<version>_linux_<arch>.tar.gz --repo addisonhuddy/kfuse
```

Docker images on Docker Hub are built by the same workflow from digest-pinned
base images but are not yet signed.

## Automated checks

Every push and pull request runs `govulncheck` (dependency vulnerabilities),
`gosec` via golangci-lint, and CodeQL for Go and GitHub Actions. Dependabot
keeps Go modules, Actions, and Docker base images current.

## Supported versions

Only the latest release receives security fixes. During the alpha period,
expect fixes on the newest release only; stored state compatibility across
versions is not guaranteed (see the README compatibility policy).
