#!/usr/bin/env bash
# Read-only verification of the repository protections described in
# RELEASING.md (issue #85).
#
# Usage:
#   GH_TOKEN=<admin PAT> scripts/verify_repo_protections.sh [owner/repo]
#
# Prints PASS / FAIL / SKIPPED per acceptance item. SKIPPED means the item is
# legitimately unavailable to this token or repo visibility (e.g. a private
# repo has no private-vulnerability-reporting endpoint, and a read-only token
# cannot read admin endpoints). Exit 0 when nothing FAILs.
#
# Never prints secrets: only status enums, booleans, and check names.

set -uo pipefail

REPO=${1:-addisonhuddy/kfuse}
PASS=0
FAIL=0
SKIP=0

if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: gh CLI is required" >&2
  exit 2
fi

# api <endpoint> [extra gh api args...]: sets API_RC, API_OUT.
api() {
  local endpoint=$1; shift
  API_OUT=$(gh api "$endpoint" "$@" 2>&1)
  API_RC=$?
}

# api_jq <endpoint> <jq filter>: like api, output filtered.
api_jq() {
  local endpoint=$1 filter=$2
  API_OUT=$(gh api "$endpoint" --jq "$filter" 2>&1)
  API_RC=$?
}

api_status() { # classify the last api()/api_jq() result
  if [ "$API_RC" -eq 0 ]; then echo ok; return; fi
  case "$API_OUT" in
    *"HTTP 403"*|*"Resource not accessible"*) echo forbidden ;;
    *"HTTP 404"*|*"Not Found"*)              echo notfound ;;
    *)                                       echo error ;;
  esac
}

pass() { PASS=$((PASS + 1)); printf 'PASS    %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL    %s\n' "$1"; }
skip() { SKIP=$((SKIP + 1)); printf 'SKIPPED %s (%s)\n' "$1" "$2"; }

echo "== verify_repo_protections: $REPO =="

# --- repository reachable + visibility -------------------------------------
api "repos/$REPO"
if [ "$(api_status)" != ok ]; then
  echo "ERROR: cannot read repos/$REPO ($(api_status)). Check GH_TOKEN/gh auth." >&2
  exit 2
fi
api_jq "repos/$REPO" '.visibility'; VISIBILITY=$API_OUT
api_jq "repos/$REPO" '.private';    VIS_PRIVATE=$API_OUT
echo "repo visibility: ${VISIBILITY:-unknown}"

# --- 1. branch protection on main -------------------------------------------
api "repos/$REPO/branches/main/protection"
case "$(api_status)" in
  ok)
    api_jq "repos/$REPO/branches/main/protection" \
      '.required_status_checks.contexts // [] | join(",")'
    CONTEXTS=$API_OUT
    api_jq "repos/$REPO/branches/main/protection" \
      '.required_pull_request_reviews != null'
    [ "$API_OUT" = true ] \
      && pass "branch protection: pull requests required" \
      || fail "branch protection: pull-request reviews not required"

    # Required checks must be real ci.yml job ids that run on pull_request.
    # 'integration' must NOT be required: it never runs on PR events.
    want="unit fuse docker local-integration"
    missing=""
    for c in $want; do
      case ",$CONTEXTS," in *",$c,"*) ;; *) missing="$missing $c" ;; esac
    done
    if [ -n "$missing" ]; then
      fail "branch protection: missing required checks:${missing} (have: ${CONTEXTS:-none})"
    elif printf '%s' ",$CONTEXTS," | grep -q ',integration,'; then
      fail "branch protection: 'integration' must not be a required check (never runs on PRs)"
    else
      pass "branch protection: required checks = $CONTEXTS"
    fi

    api_jq "repos/$REPO/branches/main/protection" \
      '.allow_force_pushes.enabled // true'
    [ "$API_OUT" = false ] \
      && pass "branch protection: force pushes disallowed" \
      || fail "branch protection: force pushes allowed"
    api_jq "repos/$REPO/branches/main/protection" \
      '.allow_deletions.enabled // true'
    [ "$API_OUT" = false ] \
      && pass "branch protection: branch deletion disallowed" \
      || fail "branch protection: branch deletion allowed"
    api_jq "repos/$REPO/branches/main/protection" \
      '.enforce_admins.enabled // true'
    if [ "$API_OUT" = false ]; then
      pass "branch protection: admin bypass available (maintainer emergency path)"
    else
      echo "NOTE    enforce_admins is on; maintainer bypass policy does not apply"
    fi
    ;;
  forbidden)
    skip "branch protection on main" "token cannot read branch protection (HTTP 403)"
    ;;
  notfound)
    fail "branch protection on main: unprotected (HTTP 404)"
    ;;
  *) fail "branch protection on main: query failed" ;;
esac

# --- 2. release tag protection ----------------------------------------------
TAG_OK=no
TAG_SKIP_REASON=""
api "repos/$REPO/tags/protection"
case "$(api_status)" in
  ok)
    api_jq "repos/$REPO/tags/protection" '.[].pattern'
    for p in $API_OUT; do
      case "v1.0.0" in $p) TAG_OK=yes ;; esac
    done
    ;;
  forbidden) TAG_SKIP_REASON="token cannot read tag protection (HTTP 403)" ;;
esac

if [ "$TAG_OK" != yes ]; then
  api "repos/$REPO/rulesets"
  if [ "$(api_status)" = ok ]; then
    api_jq "repos/$REPO/rulesets" '.[].id'
    for id in $API_OUT; do
      api_jq "repos/$REPO/rulesets/$id" '.target'
      [ "$(api_status)" = ok ] || continue
      [ "$API_OUT" = tag ] || continue
      api_jq "repos/$REPO/rulesets/$id" \
        '.conditions.ref_name.include // [] | join(",")'
      case "$API_OUT" in
        *"refs/tags/v*"*|*"~ALL"*) TAG_OK=yes ;;
      esac
    done
  fi
fi

if [ "$TAG_OK" = yes ]; then
  pass "release tag protection covers v* tags"
elif [ -n "$TAG_SKIP_REASON" ]; then
  skip "release tag protection" "$TAG_SKIP_REASON"
else
  fail "release tag protection: no tag protection rule or tag ruleset covers v*"
fi

# --- 3. security_and_analysis features --------------------------------------
api_jq "repos/$REPO" '.security_and_analysis // {} | length'
if [ "$API_OUT" = 0 ] || [ "$API_RC" -ne 0 ]; then
  skip "secret scanning" "security_and_analysis absent (private repo without GHAS, or token scope)"
  skip "secret scanning push protection" "same"
  skip "dependabot security updates" "same"
else
  for pair in "secret_scanning:secret scanning" \
              "secret_scanning_push_protection:secret scanning push protection" \
              "dependabot_security_updates:dependabot security updates"; do
    key=${pair%%:*}; label=${pair#*:}
    api_jq "repos/$REPO" ".security_and_analysis.$key.status // \"unset\""
    [ "$API_OUT" = enabled ] \
      && pass "$label enabled" \
      || fail "$label not enabled (status: $API_OUT)"
  done
fi

api "repos/$REPO/vulnerability-alerts"
case "$(api_status)" in
  ok)        pass "dependabot vulnerability alerts enabled" ;;  # 204
  forbidden) skip "dependabot vulnerability alerts" "token cannot read (HTTP 403)" ;;
  notfound)  fail "dependabot vulnerability alerts" ;;
  *)         fail "dependabot vulnerability alerts" ;;
esac

# --- 4. private vulnerability reporting --------------------------------------
api "repos/$REPO/private-vulnerability-reporting"
case "$(api_status)" in
  ok)
    api_jq "repos/$REPO/private-vulnerability-reporting" '.enabled'
    [ "$API_OUT" = true ] \
      && pass "private vulnerability reporting enabled" \
      || fail "private vulnerability reporting disabled"
    ;;
  forbidden) skip "private vulnerability reporting" "token cannot read (HTTP 403)" ;;
  notfound)
    if [ "$VIS_PRIVATE" = true ]; then
      skip "private vulnerability reporting" "repo is private; endpoint exists only once public — email fallback in SECURITY.md is live"
    else
      fail "private vulnerability reporting endpoint unreachable on a public repo"
    fi
    ;;
  *) fail "private vulnerability reporting" ;;
esac

# --- 5. Actions token permissions + fork PR policy ---------------------------
api "repos/$REPO/actions/permissions/workflow"
case "$(api_status)" in
  ok)
    api_jq "repos/$REPO/actions/permissions/workflow" '.default_workflow_permissions'
    [ "$API_OUT" = read ] \
      && pass "Actions default GITHUB_TOKEN is read-only" \
      || fail "Actions default GITHUB_TOKEN is not read-only ($API_OUT)"
    api_jq "repos/$REPO/actions/permissions/workflow" '.can_approve_pull_request_reviews'
    [ "$API_OUT" = false ] \
      && pass "Actions cannot approve pull-request reviews" \
      || fail "Actions can approve pull-request reviews"
    ;;
  forbidden) skip "Actions workflow permissions" "token cannot read (HTTP 403)" ;;
  *)         fail "Actions workflow permissions" ;;
esac

api "repos/$REPO/actions/permissions/fork-pr-contributor-approval"
case "$(api_status)" in
  ok)
    api_jq "repos/$REPO/actions/permissions/fork-pr-contributor-approval" '.approval_policy'
    case "$API_OUT" in
      all_external_contributors|first_time_contributors)
        pass "fork PR runs require maintainer approval ($API_OUT)" ;;
      *)
        fail "fork PR approval policy too permissive ($API_OUT)" ;;
    esac
    ;;
  forbidden) skip "fork PR approval policy" "token cannot read (HTTP 403)" ;;
  notfound)  skip "fork PR approval policy" "endpoint unavailable" ;;
  *)         fail "fork PR approval policy" ;;
esac

echo "== result: $PASS passed, $FAIL failed, $SKIP skipped =="
[ "$FAIL" -eq 0 ]
