#!/usr/bin/env bash
# kfuse demo launcher. Loads .env via examples/local/env.sh, builds the demo image
# (kfuse baked in), and runs the requested mode in the reference sandbox
# (Docker --privileged, /dev/fuse).
#
#   ./examples/local/run.sh                     scripted walkthrough
#   ./examples/local/run.sh --probe             FUSE + S3 probe (no Kafka)
#   ./examples/local/run.sh --shell             interactive mount + bash prompt
#   ./examples/local/run.sh --creds <file>      credentials file (default .env)
#   ./examples/local/run.sh --local             reach host-published services
#
# The credentials file uses the canonical Kafka and S3-compatible names
# (BOOTSTRAP_SERVER, KAFKA_SASL_USERNAME, S3_ACCESS_KEY, ...).
set -euo pipefail
cd "$(dirname "$0")/../.."

CREDS=""
MODE=demo
LOCAL=0

while [ $# -gt 0 ]; do
  case "$1" in
    --probe) MODE=probe ;;
    --shell) MODE=shell ;;
    --cross-host) MODE=cross-host ;;
    --local) LOCAL=1 ;;
    --creds) CREDS="$2"; shift ;;
    -h|--help)
      echo "usage: run.sh [--probe|--shell|--cross-host] [--local] [--creds <file>]"
      exit 0
      ;;
    *) echo "run.sh: unknown flag $1"; exit 1 ;;
  esac
  shift
done

[ -z "$CREDS" ] || [ -f "$CREDS" ] ||
  { echo "run.sh: credentials file $CREDS missing"; exit 1; }
source examples/local/env.sh
kfuse_load_env "${CREDS:-}"
kfuse_apply_defaults
if [ "$MODE" = probe ]; then
  kfuse_require_env S3_ACCESS_KEY S3_SECRET_KEY S3_BUCKET
else
  kfuse_require_env BOOTSTRAP_SERVER S3_ACCESS_KEY S3_SECRET_KEY S3_BUCKET
fi

echo "run.sh: building local demo image (kfuse baked in)"
docker build -q -f examples/local/Dockerfile -t kfuse-local-demo .

ENV_ARGS=(
  -e KAFKA_TLS="$KAFKA_TLS"
  -e KAFKA_TOPIC="$KAFKA_TOPIC"
  -e KAFKA_PARTITIONS="${KAFKA_PARTITIONS:-8}"
  -e BOOTSTRAP_SERVER="${BOOTSTRAP_SERVER:-}"
  -e KAFKA_SASL_USERNAME="${KAFKA_SASL_USERNAME:-}"
  -e KAFKA_SASL_PASSWORD="${KAFKA_SASL_PASSWORD:-}"
  -e S3_REGION="${S3_REGION:-}"
  -e S3_ACCESS_KEY="$S3_ACCESS_KEY"
  -e S3_SECRET_KEY="$S3_SECRET_KEY"
  -e S3_ENDPOINT="${S3_ENDPOINT:-}"
  -e S3_PATH_STYLE="${S3_PATH_STYLE:-false}"
  -e S3_BUCKET="$S3_BUCKET"
  -e S3_PREFIX="kfuse/test/local-demo/$(date +%s)-$RANDOM/"
  -e PROBE_LOWER=/work/lower
)

NET_ARGS=()
if [ "$LOCAL" = 1 ] && [ "$(uname -s)" = Linux ]; then
  NET_ARGS=(--add-host host.docker.internal:host-gateway)
fi

if [ "$MODE" = shell ]; then
  echo "run.sh: interactive session (type 'exit' to unmount and quit)"
  exec docker run -it --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${ENV_ARGS[@]}" kfuse-local-demo shell
fi

if [ "$MODE" = cross-host ]; then
  echo "run.sh: cross-host demo (two sandboxes, one session, shared S3 lease)"
  XHOST_ARGS=("${ENV_ARGS[@]}" -e KF_LOWER_ID=kfuse-cross-host-lower)

  SID=$(docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=new kfuse-local-demo cross-host | tail -1)
  [ -n "$SID" ] || { echo "run.sh: cross-host new produced no session id"; exit 1; }
  echo "run.sh: session $SID"

  docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=resume -e CROSS_SID="$SID" kfuse-local-demo cross-host

  docker run -d --rm --name kfuse-hold --privileged --device /dev/fuse "${NET_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=hold -e CROSS_SID="$SID" kfuse-local-demo cross-host >/dev/null
  sleep 3

  docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=conflict -e CROSS_SID="$SID" kfuse-local-demo cross-host

  docker stop kfuse-hold >/dev/null
  echo "CROSS-HOST PASS"
  exit 0
fi

exec docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${ENV_ARGS[@]}" kfuse-local-demo "$MODE"
