# Releasing and repository protections

This document covers two things for maintainers:

1. How to cut a release.
2. The repository protections required before kfuse accepts public
   contributions (issue #85), with the exact commands to apply each one and a
   read-only verification script.

Maintainer-only: every command below needs a token with admin access to
`addisonhuddy/kfuse` (a fine-grained PAT with **Administration: write** plus
**Actions: read**, or the owner's credentials). The verification script is
read-only and degrades gracefully with fewer permissions.

## Cutting a release

```sh
git tag v0.x.y && git push origin v0.x.y
```

`.github/workflows/release.yml` runs GoReleaser: linux/amd64 + linux/arm64
tarballs, `checksums.txt`, a GitHub Release, and a multi-arch image to
`docker.io/addisonhuddy/kfuse` (needs `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`
repository secrets).

## 1. Branch protection on `main`

Require pull requests, require the real CI checks, block force pushes and
deletion.

**Required check names must match actual CI jobs** or no PR can ever merge.
`.github/workflows/ci.yml` defines these jobs (no `name:` field, so the check
name is the job id): `unit`, `fuse`, `docker`, `local-integration`. All four
run on every `pull_request` event. Do **not** require `integration` — its
`if:` guard limits it to pushes and manual dispatch, so it never runs on PRs
and would leave required-check status permanently pending.

Apply (classic branch protection API):

```sh
gh api -X PUT repos/addisonhuddy/kfuse/branches/main/protection --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["unit", "fuse", "docker", "local-integration"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "required_approving_review_count": 1,
    "dismiss_stale_reviews": true,
    "require_last_push_approval": true
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "block_creations": false,
  "required_conversation_resolution": true,
  "lock_branch": false,
  "allow_fork_syncing": true
}
JSON
```

Verify:

```sh
gh api repos/addisonhuddy/kfuse/branches/main/protection \
  --jq '{checks: .required_status_checks.contexts,
         force_push: .allow_force_pushes.enabled,
         deletion: .allow_deletions.enabled,
         reviews: .required_pull_request_reviews.required_approving_review_count}'
```

**Bypass policy.** `enforce_admins: false` means repository admins can still
merge over red required checks in an emergency. That is the intended bypass:
only the owner account (addisonhuddy) holds admin, the bypass is recorded in
the audit log, and it must not be used for routine merges. If a stuck check
blocks a legitimate PR, fix the check — do not bypass it.

## 2. Release tag protection

Tag protection restricts creation of matching tags to users with **admin or
maintain** repository roles — collaborators with ordinary `write` access cannot
publish releases.

```sh
gh api -X POST repos/addisonhuddy/kfuse/tags/protection -f pattern='v*'
```

Verify:

```sh
gh api repos/addisonhuddy/kfuse/tags/protection --jq '.[].pattern'
```

Since `release.yml` triggers only on `v*` tags, tag protection is what gates
who can publish. Equivalent ruleset option (organization-visible, harder to
remove silently):

```sh
gh api -X POST repos/addisonhuddy/kfuse/rulesets --input - <<'JSON'
{
  "name": "release-tags",
  "target": "tag",
  "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
  "rules": [{"type": "update"}, {"type": "deletion"}, {"type": "non_fast_forward"}]
}
JSON
```

Do not add a `creation` rule to that ruleset unless it also has an
admin/maintain bypass actor, or maintainers will be unable to tag at all.

## 3. Private vulnerability reporting

SECURITY.md already advertises GitHub Security Advisories plus an email
fallback (addisonhuddy@gmail.com). The private-vulnerability-reporting API
only exists on **public** repositories — while the repo is private the
endpoint returns 404 and the email is the live channel.

Once the repo goes public:

```sh
gh api -X PUT repos/addisonhuddy/kfuse/private-vulnerability-reporting
gh api repos/addisonhuddy/kfuse/private-vulnerability-reporting --jq .enabled
# expect: true
```

Then sanity-check that
https://github.com/addisonhuddy/kfuse/security/advisories/new renders the
report form for a non-maintainer account.

## 4. Secret scanning, push protection, Dependabot

Secret scanning, push protection, and Dependabot alerts/security updates are
free on **public** repositories. On a private repository they require GitHub
Advanced Security; without GHAS the PATCH below is rejected — that is
expected, not an error to work around.

Apply once public (or with GHAS):

```sh
gh api -X PATCH repos/addisonhuddy/kfuse --input - <<'JSON'
{
  "security_and_analysis": {
    "secret_scanning": {"status": "enabled"},
    "secret_scanning_push_protection": {"status": "enabled"},
    "dependabot_security_updates": {"status": "enabled"}
  }
}
JSON
```

Dependabot alerts additionally need the vulnerability-alerts endpoint:

```sh
gh api -X PUT repos/addisonhuddy/kfuse/vulnerability-alerts
```

Verify:

```sh
gh api repos/addisonhuddy/kfuse \
  --jq '.security_and_analysis | {secret_scanning: .secret_scanning.status,
        push_protection: .secret_scanning_push_protection.status,
        dependabot_updates: .dependabot_security_updates.status}'
gh api repos/addisonhuddy/kfuse/vulnerability-alerts -i | head -1   # 204 = on
```

**Meanwhile (private, no GHAS):** run `govulncheck ./...` locally for
dependency CVEs, and rely on the `.env`-is-gitignored convention plus the
fork-PR secret isolation below. Record the gap in the sign-off checklist.

## 5. Actions token permissions and fork-PR secrets

Default `GITHUB_TOKEN` permissions must stay read-only so a compromised or
malicious workflow cannot write to the repo:

```sh
gh api -X PUT repos/addisonhuddy/kfuse/actions/permissions/workflow \
  -f default_workflow_permissions=read \
  -F can_approve_pull_request_reviews=false
```

`release.yml` declares `permissions: contents: write, packages: write` at the
workflow level, so publishing still gets write rights even with the read-only
default — the default only bounds workflows that do not ask for more.

Require maintainer approval before fork PRs run workflows at all:

```sh
gh api -X PUT repos/addisonhuddy/kfuse/actions/permissions/fork-pr-contributor-approval \
  -f approval_policy=all_external_contributors
```

**Why fork PRs never see secrets.** GitHub withholds repository secrets from
`pull_request` runs originating in forks, and this repo adds a second layer:
the `integration` job in `ci.yml` has
`if: github.event_name == 'workflow_dispatch' || github.event_name == 'push'`,
so it does not run for *any* pull request — hosted-service credentials
(`BOOTSTRAP_SERVER`, `KAFKA_*`, `S3_*`) are only ever materialized on pushes
to `main` or a manual maintainer dispatch. `local-integration` covers PR-time
integration coverage against an in-Actions Kafka/MinIO stack, so the hosted
credentials never need to be exposed to PRs. Keep it that way: never add a
`pull_request` trigger to `integration`, and never move hosted-service
secrets into the `local-integration` job.

Verify:

```sh
gh api repos/addisonhuddy/kfuse/actions/permissions/workflow \
  --jq '{default: .default_workflow_permissions, approve_prs: .can_approve_pull_request_reviews}'
gh api repos/addisonhuddy/kfuse/actions/permissions/fork-pr-contributor-approval --jq .approval_policy
```

## 6. Verify everything

```sh
GH_TOKEN=<admin PAT> scripts/verify_repo_protections.sh
```

The script prints PASS/FAIL/SKIPPED per acceptance item and exits non-zero if
any verifiable check fails. SKIPPED means the feature is legitimately
unavailable to that token or repo visibility — read the note it prints.

## Maintainer sign-off

Non-sensitive evidence only — never paste tokens, and redact anything beyond
status strings.

- [ ] Date: __________  Repo visibility at time of sign-off: __________
- [ ] Branch protection on `main` applied; required checks = `unit`, `fuse`,
      `docker`, `local-integration` (verified against `.github/workflows/ci.yml`)
- [ ] Tag protection `v*` applied (or equivalent ruleset active)
- [ ] Private vulnerability reporting verified (or repo still private —
      email fallback in SECURITY.md is the documented channel)
- [ ] Secret scanning + push protection enabled, or recorded as unavailable
      pending public visibility / GHAS: __________
- [ ] Dependabot alerts + security updates enabled, or same gap recorded
- [ ] Actions default token permissions = read; fork PR approval required
- [ ] `scripts/verify_repo_protections.sh` run with an admin token; output
      attached or linked: __________
- [ ] Signed: __________
