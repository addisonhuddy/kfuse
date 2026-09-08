#!/usr/bin/env bash
# kfuse demo launcher. Loads .env via examples/lib/env.sh, builds the demo image
# (kfuse baked in), and runs the requested mode in the reference sandbox
# (Docker --privileged, /dev/fuse).
#
#   ./examples/functional-demo/run.sh                     scripted walkthrough
#   ./examples/functional-demo/run.sh --probe             read-side FUSE probe (no Kafka)
#   ./examples/functional-demo/run.sh --shell             interactive mount + bash prompt
#   ./examples/functional-demo/run.sh --creds <file>      credentials file (default .env)
#
# The credentials file may use short names (BOOTSTRAP_SERVER, CONFLUENT_CLOUD_KEY,
# AWS_ACCESS_KEY, ...) or canonical names (KF_*, AWS_*). Hosted Confluent + AWS.
set -euo pipefail
cd "$(dirname "$0")/../.."

CREDS=""
MODE=demo

while [ $# -gt 0 ]; do
  case "$1" in
    --probe) MODE=probe ;;
    --shell) MODE=shell ;;
    --cross-host) MODE=cross-host ;;
    --creds) CREDS="$2"; shift ;;
    -h|--help)
      echo "usage: run.sh [--probe|--shell|--cross-host] [--creds <file>]"
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

echo "run.sh: building functional-demo image (kfuse baked in)"
docker build -q -f examples/functional-demo/Dockerfile -t kfuse-functional-demo .

ENV_ARGS=(
  -e KF_KAFKA_TLS="$KF_KAFKA_TLS"
  -e KF_KAFKA_TOPIC="$KF_KAFKA_TOPIC"
  -e KF_KAFKA_BROKERS="$KF_KAFKA_BROKERS"
  -e KF_KAFKA_SASL_USERNAME="$KF_KAFKA_SASL_USERNAME"
  -e KF_KAFKA_SASL_PASSWORD="$KF_KAFKA_SASL_PASSWORD"
  -e AWS_REGION="$AWS_REGION"
  -e AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID"
  -e AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY"
  -e KF_BLOB_BUCKET="$KF_BLOB_BUCKET"
  -e KF_BLOB_PREFIX="kfuse/test/functional-demo/$(date +%s)-$RANDOM/"
  -e PROBE_LOWER=/work/lower
)

if [ "$MODE" = shell ]; then
  echo "run.sh: interactive session (type 'exit' to unmount and quit)"
  exec docker run -it --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" kfuse-functional-demo shell
fi

if [ "$MODE" = cross-host ]; then
  echo "run.sh: cross-host demo (two sandboxes, one session, shared S3 lease)"
  XHOST_ARGS=("${ENV_ARGS[@]}" -e KF_LOWER_ID=kfuse-cross-host-lower)

  SID=$(docker run --rm --privileged --device /dev/fuse "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=new kfuse-functional-demo cross-host | tail -1)
  [ -n "$SID" ] || { echo "run.sh: cross-host new produced no session id"; exit 1; }
  echo "run.sh: session $SID"

  docker run --rm --privileged --device /dev/fuse "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=resume -e CROSS_SID="$SID" kfuse-functional-demo cross-host

  docker run -d --rm --name kfuse-hold --privileged --device /dev/fuse "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=hold -e CROSS_SID="$SID" kfuse-functional-demo cross-host >/dev/null
  sleep 3

  docker run --rm --privileged --device /dev/fuse "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=conflict -e CROSS_SID="$SID" kfuse-functional-demo cross-host

  docker stop kfuse-hold >/dev/null
  echo "CROSS-HOST PASS"
  exit 0
fi

exec docker run --rm --privileged --device /dev/fuse "${ENV_ARGS[@]}" kfuse-functional-demo "$MODE"
