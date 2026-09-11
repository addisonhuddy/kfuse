#!/usr/bin/env bash
# kfuse demo — the ONE script that runs INSIDE the sandbox image.
# (The host-side launcher is run.sh; it builds the image and calls this.)
#
# Modes (first argument, default "demo"):
#   demo   — scripted walkthrough (see examples/local/README.md)
#   shell  — interactive: mount a session, drop into bash, unmount on exit
#   bash   — plain shell
#   *      — exec through (e.g. `kfuse mount`)
set -euo pipefail

# Preflight helpers (copied into the image by the Dockerfile).
# shellcheck source=/dev/null
. /preflight.sh

LOG_DIR=/tmp/kfuse-demo
mkdir -p "$LOG_DIR"
MOUNT_N=0

cd /work/lower

# --- shared helpers ---------------------------------------------------------

wait_mounted() { # wait_mounted <logfile>
  local limit=${KFUSE_MOUNT_WAIT:-30} waited=0 log=$1
  while [ "$waited" -lt $((limit * 2)) ]; do
    if grep -q " /work/lower " /proc/mounts 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "$MPID" 2>/dev/null; then
      break
    fi
    sleep 0.5
    waited=$((waited + 1))
  done
  echo "FAIL: stage=mount: mount did not become live within ${limit}s" >&2
  kfuse_redact "$log" | tail -20 >&2
  kfuse_classify_failure "$log" >&2
  exit 1
}

kf() { # kf <kfuse args...> — stderr captured, classified on failure
  if kfuse "$@" 2>>"$LOG_DIR/cli.log"; then
    return 0
  fi
  kfuse_redact "$LOG_DIR/cli.log" | tail -20 >&2
  kfuse_classify_failure "$LOG_DIR/cli.log" >&2
  exit 1
}

preflight_mount() {
  kfuse_preflight_dirs /work/lower "$KF_STATE_DIR" || exit 1
  test -c /dev/fuse || { echo "stage=mount: /dev/fuse not present in the container; run with --privileged --device /dev/fuse" >&2; exit 1; }
}

mount_session() { # mount_session <sid> <outfile>
  cd /
  MOUNT_N=$((MOUNT_N + 1))
  local log="$LOG_DIR/mount-$MOUNT_N.log"
  kfuse mount "$1" --foreground --lower /work/lower >"$2" 2>"$log" &
  MPID=$!
  wait_mounted "$log"
  cd /work/lower
}

unmount_session() {
  cd /
  kill -TERM "$MPID" 2>/dev/null || true
  wait "$MPID" 2>/dev/null || true
  cd /work/lower
}

make_lower_fixture() {
  mkdir -p dir
  printf "lower-base-content" > base.txt
  printf "nested" > dir/n.txt
  ln -s base.txt link.txt
  printf "0123456789abcdefghijklmnopqrstuvwxyz" > sparse.txt
  printf "lower-attr-test-content" > lower_attr.txt
}

# --- demo: scripted walkthrough ---------------------------------------------

mode_demo() {
  local STEP=0 TOTAL=42 SID MPID
  preflight_mount

  step() {
    STEP=$((STEP + 1))
    printf '\n[%d/%d] %s\n' "$STEP" "$TOTAL" "$1"
  }
  ok() { printf '      ok\n'; }
  fail() {
    printf '      FAIL: '
    printf "$@"
    printf '\n'
    exit 1
  }
  expect_eq() { # <desc> <got> <want>
    if [ "$2" != "$3" ]; then
      fail "$1: got %q want %q" "$2" "$3"
    fi
  }
  expect_fail() { # <desc> <cmd...> — must fail
    if "$@" >/dev/null 2>&1; then
      fail "%s: expected failure, but succeeded" "$1"
    fi
  }

  step "Create a lower (base) directory fixture"
  make_lower_fixture
  ok

  step "Create a new session"
  SID=$(kf session new)
  [ -n "$SID" ] || fail "empty session id"
  printf '      session id %s\n' "$SID"
  ok

  step "Mount the session over the lower directory"
  mount_session "$SID" /tmp/mount1.out
  expect_eq "mount printed session id" "$(cat /tmp/mount1.out)" "$SID"
  ok

  step "Write a new file through the mount"
  printf "hello from overlay" > new.txt
  expect_eq "write+read round trip" "$(cat new.txt)" "hello from overlay"
  ok

  step "Append to a file across two writes"
  printf "abc" > partial.txt
  printf "def" >> partial.txt
  expect_eq "append round trip" "$(cat partial.txt)" "abcdef"
  ok

  step "Read lower files through the mount (passthrough)"
  expect_eq "base file" "$(cat base.txt)" "lower-base-content"
  expect_eq "nested file" "$(cat dir/n.txt)" "nested"
  expect_eq "symlink target" "$(cat link.txt)" "lower-base-content"
  ok

  step "Unlink a file through the mount"
  rm new.txt
  [ ! -e new.txt ] || fail "new.txt still present after unlink"
  ok

  step "Create a directory through the mount"
  mkdir work
  [ -d work ] || fail "work missing after mkdir"
  ok

  step "Create a file inside the new directory"
  printf "inside dir" > work/a.txt
  expect_eq "file under dir" "$(cat work/a.txt)" "inside dir"
  ok

  step "Rmdir of a non-empty directory fails"
  expect_fail "rmdir non-empty work" rmdir work
  ok

  step "Remove the file, then rmdir succeeds"
  rm work/a.txt
  rmdir work
  [ ! -e work ] || fail "work still present after rmdir"
  ok

  step "Unlink a lower file inside a lower directory"
  rm dir/n.txt
  [ ! -e dir/n.txt ] || fail "lower file still visible after unlink"
  expect_fail "direct path to whiteouted lower file" cat dir/n.txt
  ok

  step "Rmdir the lower directory (now empty in the merged view)"
  rmdir dir
  [ ! -e dir ] || fail "lower dir still visible after rmdir"
  ok

  step "Re-create the directory (opaque: old lower contents stay hidden)"
  mkdir dir
  [ -d dir ] || fail "re-created dir missing"
  [ -z "$(ls -A dir)" ] || fail "opaque dir must not show old lower contents"
  expect_fail "old lower child under opaque dir" cat dir/n.txt
  ok

  step "Unmount"
  unmount_session
  ok

  step "Remount the same session (resume) and verify state"
  mount_session "$SID" /tmp/mount2.out
  expect_eq "mount printed session id" "$(cat /tmp/mount2.out)" "$SID"
  [ ! -e new.txt ] || fail "unlinked file reappeared after resume"
  expect_eq "committed writes survive resume" "$(cat partial.txt)" "abcdef"
  [ ! -e work ] || fail "rmdir'd dir reappeared after resume"
  [ -d dir ] || fail "opaque dir missing after resume"
  [ -z "$(ls -A dir)" ] || fail "opaque dir resurrected lower contents after resume"
  expect_fail "old lower child hidden after resume" cat dir/n.txt
  expect_eq "lower passthrough intact" "$(cat base.txt)" "lower-base-content"
  ok

  step "Create a symlink through the mount"
  ln -s base.txt alias.txt
  expect_eq "readlink" "$(readlink alias.txt)" "base.txt"
  ok

  step "Read through the symlink"
  expect_eq "cat via symlink" "$(cat alias.txt)" "lower-base-content"
  ok

  step "Resume preserves symlinks"
  unmount_session
  mount_session "$SID" /tmp/mount3.out
  expect_eq "mount printed session id" "$(cat /tmp/mount3.out)" "$SID"
  expect_eq "readlink after resume" "$(readlink alias.txt)" "base.txt"
  expect_eq "cat via symlink after resume" "$(cat alias.txt)" "lower-base-content"
  ok

  step "Change attributes through the mount (chmod overlay file)"
  chmod 600 partial.txt
  expect_eq "mode after chmod" "$(stat -c '%a' partial.txt)" "600"
  ok

  step "Attribute override on lower-backed file"
  chmod 640 lower_attr.txt
  expect_eq "lower file mode override" "$(stat -c '%a' lower_attr.txt)" "640"
  ok

  step "Truncate an overlay file"
  truncate -s 3 partial.txt
  expect_eq "size after truncate" "$(stat -c '%s' partial.txt)" "3"
  expect_eq "content after truncate" "$(cat partial.txt)" "abc"
  ok

  step "Truncate a lower-backed file (sparse fallthrough with size)"
  truncate -s 10 lower_attr.txt
  expect_eq "lower file truncated size" "$(stat -c '%s' lower_attr.txt)" "10"
  expect_eq "lower file truncated content" "$(cat lower_attr.txt)" "lower-attr"
  ok

  step "Set timestamps through the mount (utimens)"
  touch -m -d @1700000000 partial.txt
  expect_eq "mtime after touch" "$(stat -c '%Y' partial.txt)" "1700000000"
  ok

  step "Resume preserves attributes (overlay and lower overrides)"
  unmount_session
  mount_session "$SID" /tmp/mount4.out
  expect_eq "mode after resume" "$(stat -c '%a' partial.txt)" "600"
  expect_eq "size after resume" "$(stat -c '%s' partial.txt)" "3"
  expect_eq "mtime after resume" "$(stat -c '%Y' partial.txt)" "1700000000"
  expect_eq "content after resume" "$(cat partial.txt)" "abc"
  expect_eq "lower mode override after resume" "$(stat -c '%a' lower_attr.txt)" "640"
  expect_eq "lower truncated size after resume" "$(stat -c '%s' lower_attr.txt)" "10"
  expect_eq "lower truncated content after resume" "$(cat lower_attr.txt)" "lower-attr"
  ok

  step "Rename a file through the mount"
  mv partial.txt renamed.txt
  [ ! -e partial.txt ] || fail "old name still present after rename"
  expect_eq "content at new name" "$(cat renamed.txt)" "abc"
  ok

  step "Rename over an existing file"
  printf "keep-me" > target.txt
  mv renamed.txt target.txt
  expect_eq "overwritten content" "$(cat target.txt)" "abc"
  ok

  step "Rename a lower-backed file (whiteout at old path)"
  mv base.txt moved-base.txt
  [ ! -e base.txt ] || fail "lower file still visible after rename"
  expect_eq "content at new path" "$(cat moved-base.txt)" "lower-base-content"
  ok

  step "Rename a directory through the mount"
  mkdir src
  printf "payload" > src/p.txt
  mv src dst
  [ ! -e src ] || fail "old dir still present after rename"
  expect_eq "child under renamed dir" "$(cat dst/p.txt)" "payload"
  ok

  step "Resume preserves renames"
  unmount_session
  mount_session "$SID" /tmp/mount5.out
  [ ! -e partial.txt ] || fail "renamed-away file reappeared"
  expect_eq "renamed content survives" "$(cat target.txt)" "abc"
  [ ! -e base.txt ] || fail "lower whiteout lost after resume"
  expect_eq "renamed lower content survives" "$(cat moved-base.txt)" "lower-base-content"
  expect_eq "renamed dir child survives" "$(cat dst/p.txt)" "payload"
  ok

  step "Write into the middle of a lower file (sparse CoW)"
  printf 'XXXXX' | dd of=sparse.txt bs=1 seek=10 conv=notrunc status=none
  expect_eq "merged sparse read" "$(cat sparse.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  expect_eq "lower size preserved" "$(stat -c '%s' sparse.txt)" "36"
  expect_eq "mode preserved after sparse CoW" "$(stat -c '%a' sparse.txt)" "644"
  ok

  step "Resume preserves sparse extents"
  unmount_session
  mount_session "$SID" /tmp/mount6.out
  expect_eq "merged sparse read after resume" "$(cat sparse.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  expect_eq "lower size preserved after resume" "$(stat -c '%s' sparse.txt)" "36"
  unmount_session
  ok

  step "Branch a child session (kfuse session branch)"
  local CHILD_SID
  CHILD_SID=$(kf session branch "$SID")
  [ -n "$CHILD_SID" ] || fail "empty child session id"
  mount_session "$CHILD_SID" /tmp/mount_child.out
  expect_eq "child sees parent sparse file" "$(cat sparse.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  printf "child exclusive write" > child_only.txt
  expect_eq "child can write" "$(cat child_only.txt)" "child exclusive write"
  unmount_session
  ok

  step "Parent session does not see child writes"
  mount_session "$SID" /tmp/mount_parent.out
  [ ! -e child_only.txt ] || fail "parent should not see child write"
  expect_eq "parent files unchanged" "$(cat sparse.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  unmount_session
  ok

  step "Force a state image checkpoint (kfuse checkpoint)"
  mount_session "$SID" /tmp/mount_ckpt.out
  CKPT_OFF=$(kf checkpoint "$SID")
  [ -n "$CKPT_OFF" ] || fail "empty checkpoint offset"
  printf "post checkpoint write" > post_ckpt.txt
  expect_eq "post checkpoint write visible" "$(cat post_ckpt.txt)" "post checkpoint write"
  unmount_session
  ok

  step "Resume from state image + tail event"
  mount_session "$SID" /tmp/mount_ckpt2.out
  expect_eq "checkpointed content present" "$(cat sparse.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  expect_eq "post checkpoint content present" "$(cat post_ckpt.txt)" "post checkpoint write"
  unmount_session
  ok

  step "Branch at a checkpoint offset (time travel)"
  local CHILD2_SID
  CHILD2_SID=$(kf session branch "$SID" --to "$CKPT_OFF")
  [ -n "$CHILD2_SID" ] || fail "empty time-travel child id"
  mount_session "$CHILD2_SID" /tmp/mount_child2.out
  expect_eq "pre-checkpoint file present in child" "$(cat sparse.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  [ ! -e post_ckpt.txt ] || fail "post-checkpoint write must not appear at the branch point"
  unmount_session
  ok

  step "List sessions for this lower (kfuse session ls)"
  local LS_OUT
  LS_OUT=$(kf session ls)
  printf '%s\n' "$LS_OUT" | grep -qx "$SID" || fail "session ls missing parent %s" "$SID"
  printf '%s\n' "$LS_OUT" | grep -qx "$CHILD_SID" || fail "session ls missing child %s" "$CHILD_SID"
  printf '%s\n' "$LS_OUT" | grep -qx "$CHILD2_SID" || fail "session ls missing time-travel child %s" "$CHILD2_SID"
  ok

  step "Rename a sparse-written file (holes read original lower)"
  mount_session "$SID" /tmp/mount_rn1.out
  mv sparse.txt sparse-renamed.txt
  [ ! -e sparse.txt ] || fail "sparse file still present after rename"
  expect_eq "holes read original lower after rename" "$(cat sparse-renamed.txt)" "0123456789XXXXXfghijklmnopqrstuvwxyz"
  mv sparse-renamed.txt sparse.txt
  unmount_session
  ok

  step "Rename a lower file with attr override (mode + holes persist)"
  mount_session "$SID" /tmp/mount_rn2.out
  mv lower_attr.txt lower_attr_renamed.txt
  [ ! -e lower_attr.txt ] || fail "lower file still present after rename"
  expect_eq "mode override survives rename" "$(stat -c '%a' lower_attr_renamed.txt)" "640"
  expect_eq "truncated content survives rename" "$(cat lower_attr_renamed.txt)" "lower-attr"
  mv lower_attr_renamed.txt lower_attr.txt
  unmount_session
  ok

  step "Write past lower EOF (sparse extension)"
  mount_session "$SID" /tmp/mount7.out
  printf 'ZZZZZ' | dd of=sparse.txt bs=1 seek=40 conv=notrunc status=none
  expect_eq "size extended past EOF" "$(stat -c '%s' sparse.txt)" "45"
  expect_eq "new bytes at offset 40" "$(dd if=sparse.txt bs=1 skip=40 count=5 status=none)" "ZZZZZ"
  unmount_session
  ok

  step "Recreate a lower file that was renamed away (whiteout)"
  mount_session "$SID" /tmp/mount8.out
  printf "recreated" > base.txt
  expect_eq "recreated file reads new content" "$(cat base.txt)" "recreated"
  expect_eq "renamed twin still intact" "$(cat moved-base.txt)" "lower-base-content"
  unmount_session
  ok

  printf '\nCOMPLETE (%d/%d)\n' "$STEP" "$TOTAL"
}

# --- cross-host: two sandboxes, one session -------------------------------

mode_crosshost() {
  local SID
  preflight_mount
  make_lower_fixture
  case "${CROSS_STEP:-new}" in
    new)
      SID=$(kf session new)
      mount_session "$SID" /tmp/mount.out
      printf "hello from host A" > marker.txt
      [ "$(cat marker.txt)" = "hello from host A" ] || { echo "FAIL: marker write"; exit 1; }
      unmount_session
      printf '%s\n' "$SID"
      ;;
    resume)
      SID="${CROSS_SID:?cross-host resume needs CROSS_SID}"
      mount_session "$SID" /tmp/mount.out
      [ "$(cat marker.txt)" = "hello from host A" ] || { echo "FAIL: cross-host resume: marker mismatch"; exit 1; }
      unmount_session
      echo "CROSS-HOST RESUME OK"
      ;;
    hold)
      SID="${CROSS_SID:?cross-host hold needs CROSS_SID}"
      mount_session "$SID" /tmp/mount.out
      echo "HOLDING $SID"
      trap 'unmount_session; exit 0' TERM INT
      while :; do sleep 1; done
      ;;
    conflict)
      SID="${CROSS_SID:?cross-host conflict needs CROSS_SID}"
      if kfuse mount "$SID" --foreground --lower /work/lower >/tmp/conflict.out 2>&1; then
        echo "CONFLICT FAIL: second mount unexpectedly succeeded"; exit 1
      fi
      grep -q "locked" /tmp/conflict.out || { echo "CONFLICT FAIL: no lock error:"; cat /tmp/conflict.out; exit 1; }
      echo "SESSION LOCKED OK"
      ;;
    *)
      echo "unknown CROSS_STEP $CROSS_STEP"; exit 1 ;;
  esac
}

# --- shell: interactive -------------------------------------------------------

mode_shell() {
  preflight_mount
  make_lower_fixture
  local SID
  SID=$(kf session new)
  mount_session "$SID" /tmp/mount.out
  trap unmount_session EXIT
  cat <<EOF

==================================================================
 kfuse interactive demo — session $SID

 /work/lower is a live kfuse mount over the lower directory.
 Try:  ls -la          (lower files + anything you add)
       printf "hi" > scratch.txt
       cat base.txt
       rm scratch.txt
 Type 'exit' to unmount (your committed writes are durable).
==================================================================

EOF
  bash -i
}

# --- dispatch -----------------------------------------------------------------

case "${1:-demo}" in
  demo) mode_demo ;;
  shell) mode_shell ;;
  cross-host) mode_crosshost ;;
  bash) exec bash ;;
  *) exec "$@" ;;
esac
