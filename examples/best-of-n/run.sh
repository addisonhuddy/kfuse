#!/usr/bin/env bash
# kfuse best-of-n launcher. Loads .env via examples/lib/env.sh, builds the
# best-of-n image (kfuse + toy project baked in), and runs the agent best-of-N
# story across sandboxes:
#
#   ./examples/best-of-n/run.sh                  parallel hypothesis rollouts
#   ./examples/best-of-n/run.sh --creds <file>   credentials file (default .env)
#
# One parent sandbox reproduces a bug and checkpoints. Three hypothesis
# sandboxes then run in parallel, each mounting its own child session and
# trying a different patch. A judge sandbox confirms the parent stayed
# untouched. Same Kafka + S3-compatible configuration as functional-demo.
set -euo pipefail
cd "$(dirname "$0")/../.."

CREDS=""

while [ $# -gt 0 ]; do
  case "$1" in
    --creds) CREDS="$2"; shift ;;
    -h|--help)
      echo "usage: run.sh [--creds <file>]"
      exit 0
      ;;
    *) echo "run.sh: unknown flag $1"; exit 1 ;;
  esac
  shift
done

[ -z "$CREDS" ] || [ -f "$CREDS" ] ||
  { echo "run.sh: credentials file $CREDS missing"; exit 1; }
source examples/lib/env.sh
kfuse_env_init "${CREDS:-}"

echo "run.sh: building best-of-n image (kfuse + toy project baked in)"
docker build -q -f examples/best-of-n/Dockerfile -t kfuse-best-of-n .

ENV_ARGS=(
  -e KAFKA_TLS="$KAFKA_TLS"
  -e KAFKA_TOPIC="$KAFKA_TOPIC"
  -e KAFKA_PARTITIONS="${KAFKA_PARTITIONS:-8}"
  -e BOOTSTRAP_SERVER="$BOOTSTRAP_SERVER"
  -e KAFKA_SASL_USERNAME="${KAFKA_SASL_USERNAME:-}"
  -e KAFKA_SASL_PASSWORD="${KAFKA_SASL_PASSWORD:-}"
  -e S3_REGION="${S3_REGION:-}"
  -e S3_ACCESS_KEY="$S3_ACCESS_KEY"
  -e S3_SECRET_KEY="$S3_SECRET_KEY"
  -e S3_ENDPOINT="${S3_ENDPOINT:-}"
  -e S3_PATH_STYLE="${S3_PATH_STYLE:-false}"
  -e S3_BUCKET="$S3_BUCKET"
  -e S3_PREFIX="kfuse/test/best-of-n/$(date +%s)-$RANDOM/"
  -e KF_LOWER_ID=kfuse-best-of-n-lower
)

# --- 1. parent: reproduce, checkpoint, branch -------------------------------
echo "run.sh: parent sandbox (reproduce + checkpoint + branch x3)"
PARENT_OUT=$(mktemp)
docker run --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" \
  kfuse-best-of-n parent >"$PARENT_OUT" 2>/tmp/bestofn-parent.err

SID=$(grep '^SID=' "$PARENT_OUT" | tail -1 | cut -d= -f2)
REPRO=$(grep '^REPRO=' "$PARENT_OUT" | tail -1 | cut -d= -f2)
A=$(grep '^A=' "$PARENT_OUT" | tail -1 | cut -d= -f2)
B=$(grep '^B=' "$PARENT_OUT" | tail -1 | cut -d= -f2)
C=$(grep '^C=' "$PARENT_OUT" | tail -1 | cut -d= -f2)
[ -n "$SID" ] && [ -n "$A" ] && [ -n "$B" ] && [ -n "$C" ] || {
  echo "run.sh: parent produced no session ids; output was:"
  cat "$PARENT_OUT"
  cat /tmp/bestofn-parent.err
  exit 1
}
sed -n '/^parent:/p' "$PARENT_OUT" | sed 's/^/      /'
rm -f "$PARENT_OUT"

# --- 2. three hypothesis sandboxes, in parallel -----------------------------
echo "run.sh: three hypothesis sandboxes in parallel (A, B, C)"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP" /tmp/bestofn-parent.err' EXIT

docker run --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" \
  -e BESTOFN_SID="$A" -e BESTOFN_HYPO=A kfuse-best-of-n hypo >"$TMP/a.out" 2>"$TMP/a.err" &
PA=$!
docker run --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" \
  -e BESTOFN_SID="$B" -e BESTOFN_HYPO=B kfuse-best-of-n hypo >"$TMP/b.out" 2>"$TMP/b.err" &
PB=$!
docker run --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" \
  -e BESTOFN_SID="$C" -e BESTOFN_HYPO=C kfuse-best-of-n hypo >"$TMP/c.out" 2>"$TMP/c.err" &
PC=$!

set +e
wait $PA; EA=$?
wait $PB; EB=$?
wait $PC; EC=$?
set -e

score() { grep '^BESTOFN_SCORE=' "$1" | tail -1 | cut -d= -f2; }
SA=$(score "$TMP/a.out"); SB=$(score "$TMP/b.out"); SC=$(score "$TMP/c.out")

if [ "$EA" != 0 ] || [ "$EB" != 0 ] || [ "$EC" != 0 ]; then
  echo "run.sh: a hypothesis sandbox errored (A=$EA B=$EB C=$EC)"; exit 1
fi
printf '      hypothesis A -> %s\n' "$SA"
printf '      hypothesis B -> %s\n' "$SB"
printf '      hypothesis C -> %s\n' "$SC"

[ "$SA" = "fail" ] || { echo "run.sh: hypothesis A should fail"; exit 1; }
[ "$SB" = "fail" ] || { echo "run.sh: hypothesis B should fail"; exit 1; }
[ "$SC" = "pass" ] || { echo "run.sh: hypothesis C should pass"; exit 1; }

# --- 3. judge: parent untouched, session ls sees everyone --------------------
echo "run.sh: judge sandbox (parent still buggy, no leakage, session ls)"
docker run --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" \
  -e BESTOFN_SID="$SID" -e BESTOFN_A="$A" -e BESTOFN_B="$B" -e BESTOFN_C="$C" \
  kfuse-best-of-n judge

echo "BEST-OF-N PASS"
