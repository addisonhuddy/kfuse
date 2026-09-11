#!/usr/bin/env bash
# Stop the local Kafka + MinIO stack. Pass --volumes to delete both named
# volumes (destructive: asks for confirmation unless --yes is given).
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
VOLUMES=0
YES=0
for arg in "$@"; do
  case "$arg" in
    -v|--volumes) VOLUMES=1 ;;
    -y|--yes) YES=1 ;;
    -h|--help) echo "usage: down.sh [--volumes] [--yes]"; exit 0 ;;
    *) echo "usage: down.sh [--volumes] [--yes]" >&2; exit 2 ;;
  esac
done

if [ "$VOLUMES" = 1 ]; then
  cat <<'EOF' >&2
down.sh: --volumes will delete:
  - the Kafka named volume (all local Kafka records/topics)
  - the MinIO named volume (all local objects in the bucket)
  This destroys every local session's history; treat it as a fresh
  environment. Hosted buckets and topics are never touched.
EOF
  if [ "$YES" != 1 ]; then
    if [ -t 0 ]; then
      read -r -p "Type 'yes' to continue: " answer
      [ "$answer" = yes ] || { echo "down.sh: aborted" >&2; exit 2; }
    else
      echo "down.sh: refusing --volumes without --yes on a non-interactive stdin" >&2
      exit 2
    fi
  fi
fi

set -a
# shellcheck source=examples/local/local.env
. "$DIR/local.env"
set +a

ARGS=(down)
[ "$VOLUMES" = 1 ] && ARGS+=(-v)
docker compose -f "$DIR/docker-compose.yaml" "${ARGS[@]}"
