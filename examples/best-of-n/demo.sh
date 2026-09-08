#!/usr/bin/env bash
# kfuse best-of-n — the ONE script that runs INSIDE the sandbox image.
# (The host-side launcher is run.sh; it builds the image and calls this.)
#
# Modes (first argument):
#   parent  — reproduce the bug, checkpoint, branch three hypotheses, print ids
#   hypo    — mount one child session, apply a hypothesis patch, run the test
#   judge   — remount the parent, verify isolation + that session ls sees everyone
#   bash    — plain shell
#
# Story: a config parser (parse.sh) leaks comment lines into its key list.
# The parent reproduces the bug, checkpoints, and forks three child sessions
# from that offset. Three hypothesis sandboxes each try a different patch;
# only one passes test.sh. The judge confirms the parent stayed untouched.
set -euo pipefail

cd /work/lower

# --- shared helpers ---------------------------------------------------------

wait_mounted() {
  for _ in $(seq 1 60); do
    if grep -q " /work/lower " /proc/mounts 2>/dev/null; then
      return 0
    fi
    sleep 0.5
  done
  echo "FAIL: mount never became live" >&2
  exit 1
}

mount_session() { # mount_session <sid> <outfile>
  cd /
  kfuse mount "$1" --foreground --lower /work/lower >"$2" &
  MPID=$!
  wait_mounted
  cd /work/lower
}

unmount_session() {
  cd /
  kill -TERM "$MPID" 2>/dev/null || true
  wait "$MPID" 2>/dev/null || true
  cd /work/lower
}

# --- parent: reproduce + checkpoint + branch --------------------------------

mode_parent() {
  echo "parent: creating a session and reproducing the bug"
  SID=$(kfuse session new)
  mount_session "$SID" /tmp/mount.out

  if ./test.sh >/tmp/parent-test.out 2>&1; then
    echo "PARENT FAIL: buggy parser passed tests (fixture is not actually buggy)" >&2
    exit 1
  fi
  cat /tmp/parent-test.out | sed 's/^/      /'

  echo "parent: agent leaves a repro note in the dirty workspace"
  printf 'repro: parse.sh leaks comment lines into keys\n' > repro.log
  cat repro.log | sed 's/^/      /'

  echo "parent: checkpoint -> REPRO offset"
  REPRO=$(kfuse checkpoint "$SID")
  unmount_session

  echo "parent: branching three hypotheses from REPRO=$REPRO"
  A=$(kfuse session branch "$SID" --to "$REPRO")
  B=$(kfuse session branch "$SID" --to "$REPRO")
  C=$(kfuse session branch "$SID" --to "$REPRO")

  # parseable lines for run.sh
  printf 'SID=%s\n' "$SID"
  printf 'REPRO=%s\n' "$REPRO"
  printf 'A=%s\n' "$A"
  printf 'B=%s\n' "$B"
  printf 'C=%s\n' "$C"
}

# --- hypo: mount a child, apply one patch, score it -------------------------

mode_hypo() {
  SID="${BESTOFN_SID:?}"
  H="${BESTOFN_HYPO:?}"

  mount_session "$SID" /tmp/mount.out

  case "$H" in
    A)
      cat > parse.sh <<'PATCH'
#!/usr/bin/env bash
# Hypothesis A: skip comments that start in column 0.
grep -v '^#' "$1" | cut -d= -f1
PATCH
      ;;
    B)
      cat > parse.sh <<'PATCH'
#!/usr/bin/env bash
# Hypothesis B: drop blank lines (but comment lines still leak).
grep -v '^[[:space:]]*$' "$1" | cut -d= -f1
PATCH
      ;;
    C)
      cat > parse.sh <<'PATCH'
#!/usr/bin/env bash
# Hypothesis C: skip blank lines and comments, including indented ones.
grep -vE '^[[:space:]]*(#|$)' "$1" | cut -d= -f1
PATCH
      ;;
    *)
      echo "hypo: unknown hypothesis $H" >&2
      exit 1
      ;;
  esac
  chmod +x parse.sh

  if ./test.sh >/tmp/hypo-test.out 2>&1; then
    printf 'BESTOFN_SCORE=pass\n'
  else
    printf 'BESTOFN_SCORE=fail\n'
  fi
  cat /tmp/hypo-test.out | sed 's/^/      /'

  unmount_session
}

# --- judge: parent still broken, no leakage, session ls sees everyone --------

mode_judge() {
  SID="${BESTOFN_SID:?}"

  mount_session "$SID" /tmp/mount.out

  if ./test.sh >/tmp/judge-test.out 2>&1; then
    echo "JUDGE FAIL: parent should still be buggy after children ran" >&2
    exit 1
  fi
  [ -f repro.log ] || { echo "JUDGE FAIL: repro.log missing from parent" >&2; exit 1; }
  [ ! -e winner.txt ] || { echo "JUDGE FAIL: winner leaked into parent" >&2; exit 1; }

  LS=$(kfuse session ls)
  for id in "$SID" "${BESTOFN_A:?}" "${BESTOFN_B:?}" "${BESTOFN_C:?}"; do
    printf '%s\n' "$LS" | grep -qx "$id" || { echo "JUDGE FAIL: session ls missing $id" >&2; exit 1; }
  done

  unmount_session
  echo "JUDGE OK: parent still buggy, repro.log intact, session ls sees parent + 3 children"
}

# --- dispatch ---------------------------------------------------------------

case "${1:-bash}" in
  parent) mode_parent ;;
  hypo) mode_hypo ;;
  judge) mode_judge ;;
  bash) exec bash ;;
  *) echo "best-of-n: unknown mode $1" >&2; exit 1 ;;
esac
