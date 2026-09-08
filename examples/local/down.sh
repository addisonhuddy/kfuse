#!/usr/bin/env bash
# Stop the local Kafka + MinIO stack. Pass --volumes to delete both named volumes.
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
set -a
# shellcheck source=examples/local/local.env
. "$DIR/local.env"
set +a
ARGS=(down)
case "${1:-}" in
  "") ;;
  -v|--volumes) ARGS+=(-v) ;;
  *) echo "usage: down.sh [--volumes]" >&2; exit 2 ;;
esac

docker compose -f "$DIR/docker-compose.yaml" "${ARGS[@]}"
