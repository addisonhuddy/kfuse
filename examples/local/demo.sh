#!/usr/bin/env bash
# Run the functional demo against local Apache Kafka (KRaft) and MinIO.
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)

if [ "$(uname -s)" != Linux ]; then
  echo "local demo: requires Linux because the demo needs a host /dev/fuse" >&2
  exit 1
fi

"$DIR/up.sh"

# The demo container must use the Docker-facing endpoints even when the shell
# has already sourced local.env, so container.env wins over inherited values.
set -a
# shellcheck disable=SC1091
. "$DIR/container.env"
set +a

"$ROOT/examples/functional-demo/run.sh" --local --creds "$DIR/container.env" "$@"
