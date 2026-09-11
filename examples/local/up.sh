#!/usr/bin/env bash
# Start the local Kafka + MinIO stack and wait until it is usable.
set -euo pipefail

# shellcheck source=examples/local/preflight.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/preflight.sh"
DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
COMPOSE=(docker compose -f "$DIR/docker-compose.yaml")

kfuse_preflight_docker --needs-compose || { echo "local-up: preflight failed; fix the stage above and re-run" >&2; exit 1; }
command -v curl >/dev/null || { echo "local-up: curl is required for readiness checks" >&2; exit 1; }

set -a
# shellcheck source=examples/local/local.env
. "$DIR/local.env"
# Tests and benchmarks can request a bucket other than the documented default.
S3_BUCKET=${KF_LOCAL_BUCKET:-$S3_BUCKET}
set +a

"${COMPOSE[@]}" up -d kafka minio

WAIT_S=${KFUSE_UP_WAIT:-60}
ready=0
for _ in $(seq 1 "$WAIT_S"); do
  if "${COMPOSE[@]}" exec -T kafka /opt/kafka/bin/kafka-topics.sh \
    --bootstrap-server 127.0.0.1:9092 --list >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != 1 ]; then
  echo "local-up: Kafka did not become ready within ${WAIT_S}s" >&2
  echo "next: docker compose -f examples/local/docker-compose.yaml logs kafka|minio" >&2
  exit 1
fi

ready=0
for _ in $(seq 1 "$WAIT_S"); do
  if curl -fsS "$S3_ENDPOINT/minio/health/live" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != 1 ]; then
  echo "local-up: MinIO did not become ready within ${WAIT_S}s" >&2
  echo "next: docker compose -f examples/local/docker-compose.yaml logs kafka|minio" >&2
  exit 1
fi

"${COMPOSE[@]}" run --rm createbucket >/dev/null

cat <<EOF
local kfuse stack is up
  Kafka: $BOOTSTRAP_SERVER (containers: host.docker.internal:9094)
  S3:    $S3_ENDPOINT bucket=$S3_BUCKET
  MinIO console: http://127.0.0.1:9001

Load the environment with:
  set -a; . examples/local/local.env; set +a
EOF
