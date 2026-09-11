#!/usr/bin/env bash
# Tests for examples/local/preflight.sh and the launcher wiring around it.
# A fake `docker` on PATH is driven by FAKE_DOCKER_MODE and records every
# invocation to $FAKE_DOCKER_CALLS.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
failures=0
trap 'chmod -R u+rwx "$tmp" 2>/dev/null || true; rm -rf "$tmp"' EXIT

fakebin="$tmp/fakebin"
mkdir -p "$fakebin" "$tmp/emptybin"
cat > "$fakebin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${FAKE_DOCKER_CALLS:?}"
mode=${FAKE_DOCKER_MODE:-linux}
case "$1" in
  info)
    case "$mode" in
      hang)    sleep 30 ;;
      down)    exit 1 ;;
      desktop) echo "Docker Desktop" ;;
      *)       echo "Ubuntu 22.04.5 LTS" ;;
    esac
    ;;
  run)
    [ "${FAKE_DOCKER_RUN_FAIL:-0}" = 1 ] && exit 1
    exit 0
    ;;
  compose)
    case "$2" in
      version) echo "Docker Compose version v2.fake" ;;
      *) exit 0 ;;
    esac
    ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$fakebin/docker"
export FAKE_DOCKER_CALLS="$tmp/docker.calls"
: > "$FAKE_DOCKER_CALLS"

SH="source $ROOT/examples/local/env.sh; source $ROOT/examples/local/preflight.sh;"

run_case() {
  local name=$1 expected_status=$2
  shift 2
  local output="$tmp/$name.out" status
  if "$@" >"$output" 2>&1; then
    status=0
  else
    status=$?
  fi
  if [ "$status" -eq "$expected_status" ]; then
    printf 'ok %s\n' "$name"
  else
    printf 'FAIL %s (exit %s, expected %s)\n' "$name" "$status" "$expected_status"
    cat "$output"
    failures=$((failures + 1))
  fi
}

assert_output() {
  local name=$1 pattern=$2
  if grep -Fq -- "$pattern" "$tmp/$name.out"; then
    return 0
  fi
  printf 'FAIL %s (output missing %s)\n' "$name" "$pattern"
  cat "$tmp/$name.out"
  failures=$((failures + 1))
}

assert_no_output() {
  local name=$1 pattern=$2
  if ! grep -Fq -- "$pattern" "$tmp/$name.out"; then
    return 0
  fi
  printf 'FAIL %s (output leaked %s)\n' "$name" "$pattern"
  failures=$((failures + 1))
}

FAKE=(env PATH="$fakebin:$PATH" FAKE_DOCKER_CALLS="$FAKE_DOCKER_CALLS")

# --- docker preflight -------------------------------------------------------

run_case docker_cli_missing 1 env PATH="$tmp/emptybin" /bin/bash -c \
  "$SH kfuse_preflight_docker"
assert_output docker_cli_missing "stage=host-prereqs: docker CLI not found"

run_case docker_daemon_down 1 "${FAKE[@]}" FAKE_DOCKER_MODE=down \
  bash -c "$SH kfuse_preflight_docker"
assert_output docker_daemon_down "docker daemon unreachable"

start=$SECONDS
run_case docker_daemon_hang 1 "${FAKE[@]}" FAKE_DOCKER_MODE=hang \
  KFUSE_PREFLIGHT_TIMEOUT=1 bash -c "$SH kfuse_preflight_docker"
assert_output docker_daemon_hang "did not answer within 1s"
if [ $((SECONDS - start)) -ge 10 ]; then
  echo "FAIL docker_daemon_hang (bound not enforced: $((SECONDS - start))s)"
  failures=$((failures + 1))
fi

run_case docker_ok 0 "${FAKE[@]}" FAKE_DOCKER_MODE=linux \
  bash -c "$SH kfuse_preflight_docker"
run_case docker_compose_missing 1 env PATH="$tmp/emptybin" /bin/bash -c \
  "$SH kfuse_preflight_docker --needs-compose"
assert_output docker_compose_missing "stage=host-prereqs"

# --- fuse preflight ---------------------------------------------------------

if [ "$(uname -s)" = Linux ]; then
  run_case fuse_host_missing 1 "${FAKE[@]}" FAKE_DOCKER_MODE=linux \
    KFUSE_FUSE_DEV="$tmp/no-such-dev" bash -c "$SH kfuse_preflight_fuse"
  assert_output fuse_host_missing "stage=fuse: /dev/fuse missing"

  : > "$tmp/fake-fuse"
  run_case fuse_host_present 0 "${FAKE[@]}" FAKE_DOCKER_MODE=linux \
    KFUSE_FUSE_DEV="$tmp/fake-fuse" bash -c "$SH kfuse_preflight_fuse"
else
  echo "skip fuse host-path cases (not Linux)"
fi

# Docker Desktop: the client /dev/fuse must NOT be consulted; the engine
# probe decides.
run_case fuse_desktop_probe_ok 0 "${FAKE[@]}" FAKE_DOCKER_MODE=desktop \
  KFUSE_FUSE_DEV="$tmp/no-such-dev" bash -c "$SH kfuse_preflight_fuse"
run_case fuse_desktop_probe_fail 1 "${FAKE[@]}" FAKE_DOCKER_MODE=desktop \
  FAKE_DOCKER_RUN_FAIL=1 KFUSE_FUSE_DEV="$tmp/no-such-dev" \
  bash -c "$SH kfuse_preflight_fuse"
assert_output fuse_desktop_probe_fail "the Docker engine cannot expose /dev/fuse"

# --- config preflight -------------------------------------------------------

BASE_ENV=(env BOOTSTRAP_SERVER=broker.example.net:9092 S3_ACCESS_KEY=k \
  S3_SECRET_KEY=sekrit S3_BUCKET=kfuse-demo)

run_case config_ok 0 "${BASE_ENV[@]}" bash -c "$SH kfuse_preflight_config"

run_case config_missing 1 env -u BOOTSTRAP_SERVER -u S3_ACCESS_KEY \
  -u S3_SECRET_KEY -u S3_BUCKET bash -c "$SH kfuse_preflight_config"
assert_output config_missing "stage=config"

run_case config_placeholder 1 "${BASE_ENV[@]}" \
  BOOTSTRAP_SERVER=pkc-YOUR_CLUSTER.us-west-2.aws.confluent.cloud:9092 \
  bash -c "$SH kfuse_preflight_config"
assert_output config_placeholder "stage=config"

run_case config_tls_not_bool 1 "${BASE_ENV[@]}" KAFKA_TLS=yes \
  bash -c "$SH kfuse_preflight_config"
assert_output config_tls_not_bool "stage=config: KAFKA_TLS is not a boolean"

run_case config_endpoint_no_scheme 1 "${BASE_ENV[@]}" S3_ENDPOINT=minio:9000 \
  bash -c "$SH kfuse_preflight_config"
assert_output config_endpoint_no_scheme "stage=config: S3_ENDPOINT"

run_case config_sasl_half 1 "${BASE_ENV[@]}" KAFKA_SASL_USERNAME=user-only \
  bash -c "$SH kfuse_preflight_config"
assert_output config_sasl_half "KAFKA_SASL_USERNAME and KAFKA_SASL_PASSWORD must be set together"

run_case config_partitions_zero 1 "${BASE_ENV[@]}" KAFKA_PARTITIONS=0 \
  bash -c "$SH kfuse_preflight_config"
assert_output config_partitions_zero "stage=config: KAFKA_PARTITIONS"

run_case config_lower_id_bad 1 "${BASE_ENV[@]}" KF_LOWER_ID='bad/id' \
  bash -c "$SH kfuse_preflight_config"
assert_output config_lower_id_bad "stage=config: KF_LOWER_ID"

run_case config_no_secret_leak 1 env BOOTSTRAP_SERVER=b:9092 \
  S3_ACCESS_KEY=k S3_SECRET_KEY=topsecret-value-123 S3_BUCKET=bkt \
  KAFKA_TLS=yes bash -c "$SH kfuse_preflight_config"
assert_output config_no_secret_leak "stage=config"
assert_no_output config_no_secret_leak "topsecret-value-123"

# --- dirs preflight ---------------------------------------------------------

mkdir -p "$tmp/okdir"
run_case dirs_ok 0 bash -c "$SH kfuse_preflight_dirs '$tmp/okdir' '$tmp/newdir'"

if [ "$(id -u)" -ne 0 ]; then
  mkdir -p "$tmp/locked" "$tmp/ro-parent"
  chmod 000 "$tmp/locked"
  chmod 500 "$tmp/ro-parent"
  run_case dirs_unreadable 1 bash -c "$SH kfuse_preflight_dirs '$tmp/locked'"
  assert_output dirs_unreadable "stage=dirs: $tmp/locked is not accessible"
  run_case dirs_unwritable_parent 1 bash -c \
    "$SH kfuse_preflight_dirs '$tmp/ro-parent/child'"
  assert_output dirs_unwritable_parent "stage=dirs: $tmp/ro-parent/child is not accessible"
else
  echo "skip dirs permission cases (running as root)"
fi

# --- classifier + redaction --------------------------------------------------

classify() { # classify <name> <log-line> <want-stage>
  local name=$1 line=$2 want=$3
  printf '%s\n' "$line" > "$tmp/$name.log"
  run_case "classify_$name" 0 bash -c "$SH kfuse_classify_failure '$tmp/$name.log'"
  assert_output "classify_$name" "stage=$want"
  assert_output "classify_$name" "log: $tmp/$name.log"
}

classify auth "kafka: client has run out of available brokers to talk to: SASL authentication failed [58]" auth
classify bucket "blobstore: put: NoSuchBucket: the bucket does not exist" bucket
classify lease "registry: session locked: held by hostA" lease
classify topic "kafkalog: topic kfuse.events not found" topic
classify broker "dial tcp 1.2.3.4:9092: i/o timeout" broker
classify mount "fusermount3: mount failed: Operation not permitted" mount
classify config "config: missing required env: S3_BUCKET" config
classify unknown "something entirely unexpected" unknown

printf 'failed with secret topsecret-value-456 in the log\n' > "$tmp/secret.log"
run_case redact 0 env S3_SECRET_KEY=topsecret-value-456 \
  bash -c "$SH kfuse_redact '$tmp/secret.log'"
assert_output redact "[redacted]"
assert_no_output redact "topsecret-value-456"

# --- down.sh --volumes guard --------------------------------------------------

: > "$FAKE_DOCKER_CALLS"
run_case down_volumes_refused 2 "${FAKE[@]}" \
  bash -c "$ROOT/examples/local/down.sh --volumes </dev/null"
assert_output down_volumes_refused "refusing --volumes"
if grep -q . "$FAKE_DOCKER_CALLS"; then
  echo "FAIL down_volumes_refused (docker was invoked)"
  cat "$FAKE_DOCKER_CALLS"
  failures=$((failures + 1))
fi

run_case down_volumes_yes 0 "${FAKE[@]}" \
  bash -c "$ROOT/examples/local/down.sh --volumes --yes </dev/null"
if grep -Fq "down -v" "$FAKE_DOCKER_CALLS"; then
  echo "ok down_volumes_yes docker call"
else
  echo "FAIL down_volumes_yes (expected 'down -v' in docker calls)"
  cat "$FAKE_DOCKER_CALLS"
  failures=$((failures + 1))
fi

if [ "$failures" -ne 0 ]; then
  echo "$failures preflight test(s) failed" >&2
  exit 1
fi
