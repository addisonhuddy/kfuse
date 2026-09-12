#!/usr/bin/env bash
# kfuse demo launcher. Loads .env via examples/local/env.sh, builds the demo image
# (kfuse baked in), and runs the requested mode in the reference sandbox
# (Docker --privileged, /dev/fuse).
#
#   ./examples/local/run.sh                     scripted walkthrough
#   ./examples/local/run.sh --shell             interactive mount + bash prompt
#   ./examples/local/run.sh --creds <file>      credentials file (default .env)
#   ./examples/local/run.sh --local             join the private kfuse-local Docker network
#   ./examples/local/run.sh --skip-preflight    skip host/config/FUSE checks
#
# Set KFUSE_DEMO_LOG_DIR to a host directory to bind-mount it over the
# container's /tmp/kfuse-demo so walkthrough logs survive the --rm container
# (used by CI to upload failure artifacts).
#
# The credentials file uses the canonical Kafka and S3-compatible names
# (BOOTSTRAP_SERVER, KAFKA_SASL_USERNAME, S3_ACCESS_KEY, ...).
set -euo pipefail
cd "$(dirname "$0")/../.."

CREDS=""
MODE=demo
LOCAL=0
SKIP_PREFLIGHT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --shell) MODE=shell ;;
    --cross-host) MODE=cross-host ;;
    --local) LOCAL=1 ;;
    --creds) CREDS="$2"; shift ;;
    --skip-preflight) SKIP_PREFLIGHT=1 ;;
    -h|--help)
      echo "usage: run.sh [--shell|--cross-host] [--local] [--creds <file>] [--skip-preflight]"
      exit 0
      ;;
    *) echo "run.sh: unknown flag $1"; exit 1 ;;
  esac
  shift
done

[ -z "$CREDS" ] || [ -e "$CREDS" ] ||
  { echo "run.sh: credentials file $CREDS missing"; exit 1; }
# shellcheck source=examples/local/env.sh
source examples/local/env.sh
# shellcheck source=examples/local/preflight.sh
source examples/local/preflight.sh
kfuse_load_env "${CREDS:-}"
kfuse_apply_defaults

if [ "$SKIP_PREFLIGHT" != 1 ]; then
  # shellcheck disable=SC2119
  if ! kfuse_preflight_config ||
     ! kfuse_preflight_docker ||
     ! kfuse_preflight_fuse; then
    echo "run.sh: preflight failed; fix the stage above and re-run" >&2
    exit 1
  fi
  echo "run.sh: preflight ok (docker, fuse, config)"
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
)

NET_ARGS=()
if [ "$LOCAL" = 1 ] && [ "$(uname -s)" = Linux ]; then
  NET_ARGS=(--network kfuse-local)
fi

VOL_ARGS=()
if [ -n "${KFUSE_DEMO_LOG_DIR:-}" ]; then
  mkdir -p "$KFUSE_DEMO_LOG_DIR"
  VOL_ARGS=(-v "$(cd "$KFUSE_DEMO_LOG_DIR" && pwd):/tmp/kfuse-demo")
fi

demo_failed() {
  echo "run.sh: demo failed inside the container; see the stage line above. Rerun with --shell to inspect." >&2
  exit 1
}

if [ "$MODE" = shell ]; then
  echo "run.sh: interactive session (type 'exit' to unmount and quit)"
  docker run -it --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${VOL_ARGS[@]}" "${ENV_ARGS[@]}" kfuse-local-demo shell || demo_failed
  exit 0
fi

if [ "$MODE" = cross-host ]; then
  echo "run.sh: cross-host demo (two sandboxes, one session, shared S3 lease)"
  XHOST_ARGS=("${ENV_ARGS[@]}" -e KF_LOWER_ID=kfuse-cross-host-lower)

  SID=$(docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${VOL_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=new kfuse-local-demo cross-host | tail -1)
  [ -n "$SID" ] || { echo "run.sh: cross-host new produced no session id"; exit 1; }
  echo "run.sh: session $SID"

  docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${VOL_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=resume -e CROSS_SID="$SID" kfuse-local-demo cross-host || demo_failed

  docker run -d --rm --name kfuse-hold --privileged --device /dev/fuse "${NET_ARGS[@]}" "${VOL_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=hold -e CROSS_SID="$SID" kfuse-local-demo cross-host >/dev/null
  sleep 3

  docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${VOL_ARGS[@]}" "${XHOST_ARGS[@]}" \
    -e CROSS_STEP=conflict -e CROSS_SID="$SID" kfuse-local-demo cross-host || demo_failed

  docker stop kfuse-hold >/dev/null
  echo "CROSS-HOST PASS"
  exit 0
fi

docker run --rm --privileged --device /dev/fuse "${NET_ARGS[@]}" "${VOL_ARGS[@]}" "${ENV_ARGS[@]}" kfuse-local-demo "$MODE" || demo_failed
