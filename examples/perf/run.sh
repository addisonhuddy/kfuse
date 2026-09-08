#!/usr/bin/env bash
# kfuse benchmark suite (issue #35). Measures write/read latency with the
# blob-PUT / Kafka-append / apply breakdown, metadata ops, commit throughput,
# branch, resume, checkpoint cost, and daemon memory footprint, then reduces
# everything to percentile tables with analyze.
#
# .env at the repo root is loaded automatically; source .stack-env still works
# because already-exported environment values win.
#   source examples/perf/.stack-env       # or a real Kafka/S3 env
#   ./examples/perf/run.sh [--creds <file>] [out-dir]
#
# Requires FUSE on the host (/dev/fuse + fusermount3) and the standard kfuse
# env (BOOTSTRAP_SERVER, KAFKA_*, S3_*). Results land in <out-dir>
# (default examples/perf/results/<utc-timestamp>): raw JSONL per scenario,
# results.json (machine-readable, diff across PRs), results.md.
set -euo pipefail
cd "$(dirname "$0")/../.."
REPO=$PWD

CREDS=""
OUT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --creds)
      [ $# -ge 2 ] || { echo "run.sh: --creds requires a file"; exit 1; }
      CREDS=$2
      shift 2
      ;;
    -*) echo "run.sh: unknown flag $1"; exit 1 ;;
    *)
      [ -z "$OUT" ] || { echo "run.sh: multiple output directories"; exit 1; }
      OUT=$1
      shift
      ;;
  esac
done

[ -z "$CREDS" ] || [ -f "$CREDS" ] ||
  { echo "run.sh: credentials file $CREDS missing"; exit 1; }
source examples/lib/env.sh
kfuse_env_init "${CREDS:-}"

OUT=${OUT:-examples/perf/results/$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUT/raw"
OUT=$(cd "$OUT" && pwd)
RAW=$OUT/raw

WORK=$(mktemp -d /tmp/kfuse-perf.XXXXXX)
LOWER=$WORK/lower
export KF_STATE_DIR=$WORK/state
export KF_LOWER_ID=${KF_LOWER_ID:-kfuse-perf-lower}
export S3_PREFIX=${S3_PREFIX:-kfuse/perf/$(date +%s)/}

MPID=""
cleanup() {
  [ -n "$MPID" ] && kill -TERM "$MPID" 2>/dev/null || true
  grep -q " $LOWER " /proc/mounts 2>/dev/null && fusermount3 -u "$LOWER" 2>/dev/null || true
}
trap cleanup EXIT

log() { printf '\n== %s\n' "$*"; }

log "building kfuse + loadgen + analyze (non-race)"
go build -o "$WORK/kfuse" ./cmd/kfuse
go build -o "$WORK/loadgen" ./examples/perf/loadgen
go build -o "$WORK/analyze" ./examples/perf/analyze
KFUSE=$WORK/kfuse
LOADGEN=$WORK/loadgen

log "building lower fixture (wide=1000 files, deep=64 levels, 1MiB file)"
mkdir -p "$LOWER/wide"
for i in $(seq 1 1000); do printf 'lower-%04d' "$i" > "$LOWER/wide/f$i.txt"; done
D=$LOWER/deep
for i in $(seq 1 64); do D=$D/d$i; done
mkdir -p "$D"
printf 'leaf' > "$D/leaf.txt"
mkdir -p "$LOWER/lowread"
head -c 1048576 /dev/zero | tr '\0' 'L' > "$LOWER/lowread/file1m.dat"

# mount_session <sid> <perf-log> — backgrounds a foreground mount and waits
# for it to become live. Sets MPID.
mount_session() {
  KF_PERF_LOG=$2 "$KFUSE" mount "$1" --foreground --lower "$LOWER" >>"$OUT/mount.log" 2>&1 &
  MPID=$!
  for _ in $(seq 1 120); do
    grep -q " $LOWER " /proc/mounts && return 0
    sleep 0.5
  done
  echo "run.sh: mount of $1 never became live (see $OUT/mount.log)" >&2
  exit 1
}

unmount_session() {
  kill -TERM "$MPID"
  wait "$MPID" 2>/dev/null || true
  MPID=""
  for _ in $(seq 1 60); do
    grep -q " $LOWER " /proc/mounts || return 0
    sleep 0.5
  done
  echo "run.sh: unmount never completed" >&2
  exit 1
}

new_session() { "$KFUSE" session new --lower "$LOWER" | tail -1; }

rss_kb() { awk '/VmRSS/{print $2}' "/proc/$MPID/status"; }

emit_mem() { # emit_mem <file> <label>
  printf '{"ts":%d,"ev":"mem","rss_kb":%s,"label":"%s"}\n' \
    "$(date +%s%N)" "$(rss_kb)" "$2" >> "$1"
}

# --- S1/S2: write latency, 4KiB and 64KiB -----------------------------------
log "S1: write latency 4KiB (32 files x 16 writes)"
SID=$(new_session)
mount_session "$SID" "$RAW/write4k-daemon.jsonl"
"$LOADGEN" write --dir "$LOWER" --files 32 --writes 16 --size 4096 --label 4k >> "$RAW/write4k-client.jsonl"
unmount_session

log "S2: write latency 64KiB (16 files x 8 writes)"
SID=$(new_session)
mount_session "$SID" "$RAW/write64k-daemon.jsonl"
"$LOADGEN" write --dir "$LOWER" --files 16 --writes 8 --size 65536 --label 64k >> "$RAW/write64k-client.jsonl"
unmount_session

# --- S3: read latency, cold vs warm, overlay vs lower ------------------------
log "S3: read latency (cold = fresh mount, warm = page cache)"
SID=$(new_session)
mount_session "$SID" "$RAW/readprep-daemon.jsonl"
"$LOADGEN" write --dir "$LOWER" --files 4 --writes 64 --size 4096 --label prep >/dev/null
mkdir -p "$LOWER/blobread"
for f in "$LOWER"/w-prep-*.dat; do mv "$f" "$LOWER/blobread/$(basename "$f")"; done
unmount_session
mount_session "$SID" "$RAW/read-daemon.jsonl"      # fresh mount: kernel caches empty
"$LOADGEN" read --dir "$LOWER/blobread" --files 4 --size 4096 --label cold-overlay >> "$RAW/read-client.jsonl"
"$LOADGEN" read --dir "$LOWER/blobread" --files 4 --size 4096 --label warm-overlay >> "$RAW/read-client.jsonl"
"$LOADGEN" read --dir "$LOWER/lowread" --files 1 --size 4096 --label cold-lower >> "$RAW/read-client.jsonl"
"$LOADGEN" read --dir "$LOWER/lowread" --files 1 --size 4096 --label warm-lower >> "$RAW/read-client.jsonl"
unmount_session

# --- S4: metadata ops --------------------------------------------------------
log "S4: metadata (stat/readdir/lookup; wide and deep; cold vs warm)"
SID=$(new_session)
mount_session "$SID" "$RAW/meta-daemon.jsonl"
"$LOADGEN" meta --dir "$LOWER/wide" --label cold-wide >> "$RAW/meta-client.jsonl"
"$LOADGEN" meta --dir "$LOWER/wide" --label warm-wide >> "$RAW/meta-client.jsonl"
DEEP=$LOWER/deep
for i in $(seq 1 64); do DEEP=$DEEP/d$i; done
"$LOADGEN" meta --dir "$DEEP" --label cold-deep >> "$RAW/meta-client.jsonl"
"$LOADGEN" meta --dir "$DEEP" --label warm-deep >> "$RAW/meta-client.jsonl"
unmount_session

# --- S5: commit throughput vs concurrent writers -----------------------------
log "S5: commit throughput (1/2/4/8 writers x 8s, 4KiB appends)"
SID=$(new_session)
mount_session "$SID" "$RAW/throughput-daemon.jsonl"
for W in 1 2 4 8; do
  "$LOADGEN" throughput --dir "$LOWER" --writers "$W" --seconds 8 --size 4096 >> "$RAW/throughput-client.jsonl"
done
emit_mem "$RAW/mem.jsonl" "post-throughput"
unmount_session

# --- S6: checkpoint cost + memory footprint vs overlay size ------------------
log "S6: checkpoint cost and daemon RSS vs overlay size"
SID=$(new_session)
mount_session "$SID" "$RAW/checkpoint-daemon.jsonl"
for SCALE in 100 400; do
  "$LOADGEN" write --dir "$LOWER" --files "$SCALE" --writes 1 --size 4096 --label "scale$SCALE" >/dev/null
  emit_mem "$RAW/mem.jsonl" "files=$SCALE"
  T0=$(date +%s%N)
  KF_PERF_LOG=$RAW/checkpoint-daemon.jsonl "$KFUSE" checkpoint "$SID" --lower "$LOWER" >/dev/null
  printf '{"ts":%d,"ev":"checkpoint_wall","ns":%d,"label":"files=%s"}\n' \
    "$(date +%s%N)" "$(( $(date +%s%N) - T0 ))" "$SCALE" >> "$RAW/checkpoint-client.jsonl"
done
# checkpoint while writes are in flight: does an upload stall commits?
"$LOADGEN" throughput --dir "$LOWER" --writers 2 --seconds 6 --size 4096 >> "$RAW/ckpt-impact-client.jsonl" &
LG=$!
sleep 2
KF_PERF_LOG=$RAW/checkpoint-daemon.jsonl "$KFUSE" checkpoint "$SID" --lower "$LOWER" >/dev/null
wait "$LG"
unmount_session

# --- S7: branch time vs tree size --------------------------------------------
log "S7: branch (fork) time from the S6 session's tail"
for i in 1 2 3; do
  T0=$(date +%s%N)
  KF_PERF_LOG=$RAW/branch-daemon.jsonl "$KFUSE" session branch "$SID" --lower "$LOWER" >/dev/null
  printf '{"ts":%d,"ev":"branch_wall","ns":%d,"label":"try%d"}\n' \
    "$(date +%s%N)" "$(( $(date +%s%N) - T0 ))" "$i" >> "$RAW/branch-client.jsonl"
done

# --- S8: resume/mount time, with and without a state image -------------------
log "S8: resume time (small session without image vs large session with image)"
SID_SMALL=$(new_session)
mount_session "$SID_SMALL" "$RAW/resumeprep-daemon.jsonl"
"$LOADGEN" write --dir "$LOWER" --files 25 --writes 2 --size 4096 --label small >/dev/null
unmount_session   # ~50 events, under the 100-event/30s materializer triggers
for i in 1 2 3; do
  T0=$(date +%s%N)
  mount_session "$SID_SMALL" "$RAW/resume-daemon.jsonl"
  printf '{"ts":%d,"ev":"mount_wall","ns":%d,"label":"small-noimage-try%d"}\n' \
    "$(date +%s%N)" "$(( $(date +%s%N) - T0 ))" "$i" >> "$RAW/resume-client.jsonl"
  unmount_session
done
for i in 1 2 3; do
  T0=$(date +%s%N)
  mount_session "$SID" "$RAW/resume-daemon.jsonl"   # S6 session: checkpointed
  printf '{"ts":%d,"ev":"mount_wall","ns":%d,"label":"large-image-try%d"}\n' \
    "$(date +%s%N)" "$(( $(date +%s%N) - T0 ))" "$i" >> "$RAW/resume-client.jsonl"
  unmount_session
done

# --- reduce -------------------------------------------------------------------
log "analyzing"
"$WORK/analyze" "$RAW" "$OUT"
{
  echo "commit=$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  echo "date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "host=$(uname -srm)"
  echo "go=$(go version | awk '{print $3}')"
  echo "brokers=$BOOTSTRAP_SERVER"
} > "$OUT/run-info.txt"
rm -rf "$WORK"
log "done: $OUT/results.md"
