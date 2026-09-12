#!/usr/bin/env bash
# Validate the local stack's loopback-only ports and private network wiring.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
COMPOSE=(docker compose -f "$ROOT/examples/local/docker-compose.yaml" --env-file "$ROOT/examples/local/local.env")
failures=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

ok() {
  printf 'ok %s\n' "$1"
}

fail() {
  printf 'FAIL %s\n' "$1"
  failures=$((failures + 1))
}

if ! command -v docker >/dev/null 2>&1; then
  echo "FAIL docker CLI not found; install Docker before running local network tests" >&2
  exit 1
fi

if command -v python3 >/dev/null 2>&1; then
  PARSER=python3
elif command -v jq >/dev/null 2>&1; then
  PARSER=jq
else
  echo "FAIL neither python3 nor jq is available to parse docker compose config" >&2
  exit 1
fi

config="$tmp/config.json"
if "${COMPOSE[@]}" config --format json >"$config"; then
  ok static_compose_config
else
  fail static_compose_config
fi

if [ "$PARSER" = python3 ] && [ -s "$config" ]; then
  if python3 - "$config" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    config = json.load(stream)

services = config.get("services", {})
networks = config.get("networks", {})
expected_ports = {
    "kafka": {9092},
    "minio": {9000, 9001},
    "createbucket": set(),
}
failures = 0


def check(name, condition, detail):
    global failures
    if condition:
        print(f"ok {name}")
    else:
        print(f"FAIL {name}: {detail}")
        failures += 1


check("compose_services", set(services) >= set(expected_ports), "expected kafka, minio, and createbucket")
check(
    "compose_network",
    networks.get("kfuse-local", {}).get("name") == "kfuse-local",
    "kfuse-local must have explicit name kfuse-local",
)
for service_name, ports in expected_ports.items():
    service = services.get(service_name, {})
    published = service.get("ports", [])
    published_targets = set()
    all_loopback = True
    for port in published:
        host_ip = port.get("host_ip", "")
        published_targets.add(int(port["target"]))
        all_loopback &= host_ip == "127.0.0.1"
    check(f"{service_name}_ports_loopback", all_loopback, "every published port must use host_ip 127.0.0.1")
    check(
        f"{service_name}_ports",
        published_targets == ports,
        f"expected published target ports {sorted(ports)}, got {sorted(published_targets)}",
    )
    networks = service.get("networks", {})
    if isinstance(networks, list):
        network_names = set(networks)
    else:
        network_names = set(networks)
    check(f"{service_name}_network", "kfuse-local" in network_names, "service must list kfuse-local")
    check(f"{service_name}_no_extra_hosts", "extra_hosts" not in service, "extra_hosts must be absent")

kafka_env = services.get("kafka", {}).get("environment", {})
check(
    "kafka_advertised_listeners",
    kafka_env.get("KAFKA_ADVERTISED_LISTENERS")
    == "PLAINTEXT_HOST://127.0.0.1:9092,PLAINTEXT_DOCKER://kafka:9094",
    "KAFKA_ADVERTISED_LISTENERS does not match the local network endpoints",
)
if failures:
    raise SystemExit(1)
PY
  then
    :
  else
    failures=$((failures + 1))
  fi
elif [ "$PARSER" = jq ] && [ -s "$config" ]; then
  if jq -e '
    (.services | keys) as $services |
    ($services | index("kafka") != null and index("minio") != null and index("createbucket") != null) and
    ([.services[] | (.networks | if type == "array" then . else keys end) | index("kfuse-local") != null] | all) and
    ([.services[] | has("extra_hosts") | not] | all) and
    ([.services.kafka.ports[] | .host_ip == "127.0.0.1"] | all) and
    ([.services.minio.ports[] | .host_ip == "127.0.0.1"] | all) and
    ([.services.kafka.ports[].target] | sort == [9092]) and
    ([.services.minio.ports[].target] | sort == [9000, 9001]) and
    ((.services.createbucket.ports // []) | length == 0) and
    (.services.kafka.environment.KAFKA_ADVERTISED_LISTENERS ==
      "PLAINTEXT_HOST://127.0.0.1:9092,PLAINTEXT_DOCKER://kafka:9094")
    and (.networks."kfuse-local".name == "kfuse-local")
  ' "$config" >/dev/null; then
    ok static_compose_network
  else
    fail static_compose_network
  fi
fi

check_env() {
  local name=$1 file=$2 expected=$3
  if grep -Fxq "$expected" "$file"; then
    ok "$name"
  else
    fail "$name"
  fi
}

check_env container_bootstrap "$ROOT/examples/local/container.env" 'BOOTSTRAP_SERVER=kafka:9094'
check_env container_s3 "$ROOT/examples/local/container.env" 'S3_ENDPOINT=http://minio:9000'
check_env local_bootstrap "$ROOT/examples/local/local.env" 'BOOTSTRAP_SERVER=127.0.0.1:9092'
check_env local_s3 "$ROOT/examples/local/local.env" 'S3_ENDPOINT=http://127.0.0.1:9000'

if running=$("${COMPOSE[@]}" ps --status running -q kafka 2>/dev/null) && [ -n "$running" ]; then
  kafka_ports=$(docker port kfuse-local-kafka 2>&1) || {
    fail live_kafka_ports
    kafka_ports=""
  }
  minio_ports=$(docker port kfuse-local-minio 2>&1) || {
    fail live_minio_ports
    minio_ports=""
  }
  for service in kafka minio; do
    ports_var="${service}_ports"
    ports=${!ports_var}
    if [ -n "$ports" ] &&
      ! printf '%s\n' "$ports" | grep -Eq '0\.0\.0\.0:|\[::\]:' &&
      ! printf '%s\n' "$ports" | grep -Evq '127\.0\.0\.1:'; then
      ok "live_${service}_ports"
    else
      fail "live_${service}_ports"
    fi
  done

  if docker run --rm --network kfuse-local apache/kafka:4.3.1 \
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9094 --list >/dev/null; then
    ok live_kafka_connectivity
  else
    fail live_kafka_connectivity
  fi

  if docker run --rm --network kfuse-local \
    --entrypoint /bin/sh quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z \
    -c 'mc alias set local http://minio:9000 kfuse kfuse-local-secret && mc ls local/kfuse-local' >/dev/null; then
    ok live_minio_connectivity
  else
    fail live_minio_connectivity
  fi
else
  echo "skip live checks (stack not running)"
fi

if [ "$failures" -ne 0 ]; then
  echo "$failures local network test(s) failed" >&2
  exit 1
fi
