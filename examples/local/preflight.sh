# shellcheck shell=bash
# kfuse onboarding preflight checks. Sourced library: no side effects on
# source. Every check writes diagnostics to stderr, returns non-zero on
# failure, and never exits.

# kfuse_bounded <seconds> <cmd...>: run cmd with a wall-clock bound without
# relying on coreutils `timeout` (absent on stock macOS). Returns 124 on
# timeout, else the command's exit status. Its stdout/stderr pass through.
kfuse_bounded() {
  local limit=$1 pid waited rc
  shift
  "$@" &
  pid=$!
  waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge $((limit * 5)) ]; then
      kill -TERM "$pid" 2>/dev/null || true
      sleep 1
      kill -KILL "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      return 124
    fi
    sleep 0.2
    waited=$((waited + 1))
  done
  wait "$pid" || rc=$?
  return "${rc:-0}"
}

# kfuse_preflight_docker [--needs-compose]: docker CLI present and daemon
# answering; --needs-compose also requires `docker compose` (v2).
kfuse_preflight_docker() {
  local t="${KFUSE_PREFLIGHT_TIMEOUT:-15}" rc os
  if ! command -v docker >/dev/null 2>&1; then
    echo "stage=host-prereqs: docker CLI not found; install Docker (https://docs.docker.com/get-docker/)" >&2
    return 1
  fi
  kfuse_bounded "$t" docker info -f '{{.OperatingSystem}}' >/dev/null 2>&1
  rc=$?
  if [ "$rc" -eq 124 ]; then
    echo "stage=host-prereqs: docker daemon did not answer within ${t}s" >&2
    return 1
  elif [ "$rc" -ne 0 ]; then
    echo "stage=host-prereqs: docker daemon unreachable; start Docker Desktop / \`sudo systemctl start docker\` and check \`docker info\`" >&2
    return 1
  fi
  if [ "${1:-}" = "--needs-compose" ]; then
    if ! docker compose version >/dev/null 2>&1; then
      echo "stage=host-prereqs: docker compose (v2 plugin) not found; install Docker Compose (https://docs.docker.com/compose/install/)" >&2
      return 1
    fi
  fi
  return 0
}

# kfuse_preflight_fuse: verify FUSE where the mount will actually run — the
# Docker host, which is not necessarily this client (macOS, WSL, Docker
# Desktop).
kfuse_preflight_fuse() {
  local fuse_dev="${KFUSE_FUSE_DEV:-/dev/fuse}" image="${KFUSE_PREFLIGHT_PROBE_IMAGE:-busybox:stable}" os rc
  os=$(uname -s)
  if [ "$os" = Linux ]; then
    local docker_os
    docker_os=$(docker info -f '{{.OperatingSystem}}' 2>/dev/null || true)
    if [[ "$docker_os" != *"Docker Desktop"* ]]; then
      if [ ! -e "$fuse_dev" ]; then
        echo "stage=fuse: /dev/fuse missing on the Docker host; load the fuse module (\`sudo modprobe fuse\`) or run on a Linux host with FUSE" >&2
        return 1
      fi
      return 0
    fi
  fi
  # Client is not the Docker host (macOS, WSL client, Docker Desktop): do not
  # fail for a missing client /dev/fuse; probe inside the engine instead.
  echo "stage=fuse: probing the Docker engine for /dev/fuse support (read-only probe; pulls $image)" >&2
  kfuse_bounded 60 docker run --rm --device /dev/fuse "$image" test -c /dev/fuse >/dev/null 2>&1
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "stage=fuse: the Docker engine cannot expose /dev/fuse; kfuse mounts need a Linux engine with FUSE (Docker Desktop mount support is not validated)" >&2
    return 1
  fi
  return 0
}

kfuse_bool_ok() {
  case "$1" in
    1|0|t|f|T|F|true|false|TRUE|FALSE|True|False) return 0 ;;
  esac
  return 1
}

# kfuse_preflight_config: required vars plus canonical-shape checks mirroring
# internal/config/config.go. Reports variable names only, never values.
kfuse_preflight_config() {
  local rc=0 name v
  if ! kfuse_require_env BOOTSTRAP_SERVER S3_ACCESS_KEY S3_SECRET_KEY S3_BUCKET; then
    echo "stage=config: fix the missing/placeholder variables named above in .env (see .env.example)" >&2
    rc=1
  fi
  for name in KAFKA_TLS S3_PATH_STYLE; do
    v=${!name:-}
    if [ -n "$v" ] && ! kfuse_bool_ok "$v"; then
      echo "stage=config: $name is not a boolean (want one of: true false 1 0)" >&2
      rc=1
    fi
  done
  v=${KAFKA_PARTITIONS:-}
  if [ -n "$v" ]; then
    if ! [[ "$v" =~ ^[0-9]+$ ]] || [ "$v" -le 0 ]; then
      echo "stage=config: KAFKA_PARTITIONS is not an integer >0" >&2
      rc=1
    fi
  fi
  v=${S3_ENDPOINT:-}
  if [ -n "$v" ] && ! [[ "$v" =~ ^https?://[^/]+ ]]; then
    echo "stage=config: S3_ENDPOINT must be an http:// or https:// URL" >&2
    rc=1
  fi
  if { [ -n "${KAFKA_SASL_USERNAME:-}" ] && [ -z "${KAFKA_SASL_PASSWORD:-}" ]; } ||
     { [ -z "${KAFKA_SASL_USERNAME:-}" ] && [ -n "${KAFKA_SASL_PASSWORD:-}" ]; }; then
    echo "stage=config: KAFKA_SASL_USERNAME and KAFKA_SASL_PASSWORD must be set together" >&2
    rc=1
  fi
  v=${KF_LOWER_ID:-}
  if [ -n "$v" ]; then
    # Mirrors ValidateID in internal/config/ids.go.
    if [ ${#v} -gt 128 ] || [ "$v" = "." ] || [ "$v" = ".." ] ||
       [[ "$v" =~ [^A-Za-z0-9._-] ]]; then
      echo "stage=config: KF_LOWER_ID must be a printable id (letters, digits, - _ . only, <=128 bytes)" >&2
      rc=1
    fi
  fi
  return "$rc"
}

# kfuse_preflight_dirs <dir>...: each dir must exist (or have a writable
# parent so it can be created) and be readable+writable.
kfuse_preflight_dirs() {
  local d parent
  for d in "$@"; do
    if [ -e "$d" ]; then
      if [ ! -d "$d" ]; then
        echo "stage=dirs: $d is not accessible (not a directory)" >&2
        return 1
      fi
      if [ ! -r "$d" ] || [ ! -w "$d" ]; then
        echo "stage=dirs: $d is not accessible (read/write permission denied)" >&2
        return 1
      fi
    else
      parent=$(dirname "$d")
      if [ ! -w "$parent" ]; then
        echo "stage=dirs: $d is not accessible (cannot create it: $parent is not writable)" >&2
        return 1
      fi
    fi
  done
  return 0
}

# kfuse_classify_failure <logfile>: print one stage line, a next action, and
# the log location. First match wins.
kfuse_classify_failure() {
  local log=$1
  if grep -qiE 'missing required env|config:|is not a boolean|is not an integer|must be an http' "$log"; then
    echo "stage=config: fix the variables named above in .env (see .env.example)" >&2
  elif grep -qiE 'SASL|Authentication failed|InvalidAccessKeyId|SignatureDoesNotMatch|AccessDenied|InvalidClientTokenId|\[58\]|403' "$log"; then
    echo "stage=auth: check KAFKA_SASL_* (Confluent: use a cluster Kafka API key, not a Global key) and S3_ACCESS_KEY/S3_SECRET_KEY permissions" >&2
  elif grep -qiE 'NoSuchBucket|bucket' "$log"; then
    echo "stage=bucket: create S3_BUCKET or fix its name/region (S3_REGION, S3_ENDPOINT)" >&2
  elif grep -qiE 'session locked|registry: session locked|ErrLocked|held by' "$log"; then
    echo "stage=lease: another live mount holds this session; run \`kfuse umount\` there or wait for the lease TTL" >&2
  elif grep -qiE 'topic .* not found|UNKNOWN_TOPIC|has no visible partitions|create topic' "$log"; then
    echo "stage=topic: create KAFKA_TOPIC with KAFKA_PARTITIONS partitions or grant Create/Describe ACLs" >&2
  elif grep -qiE 'dial tcp|no such host|connection refused|i/o timeout|kafkalog: connect|broker|EOF' "$log"; then
    echo "stage=broker: check BOOTSTRAP_SERVER host:port, network egress, and KAFKA_TLS (true for hosted, false for the local stack)" >&2
  elif grep -qiE 'fusermount|/dev/fuse|Operation not permitted|fuse:|mount never became live|Transport endpoint' "$log"; then
    echo "stage=mount: run in a privileged Linux container with --device /dev/fuse, or check /dev/fuse on the host" >&2
  else
    echo "stage=unknown: inspect the log" >&2
  fi
  echo "log: $log" >&2
  if [ -n "${KF_STATE_DIR:-}" ]; then
    echo "daemon log: $KF_STATE_DIR/${KF_LOWER_ID:-<lower-id>}/daemon.log" >&2
  fi
}

# kfuse_redact <file>: print the file with the current secret values replaced
# by [redacted] (literal substitution, not regex).
kfuse_redact() {
  local line name val
  while IFS= read -r line || [ -n "$line" ]; do
    for name in S3_SECRET_KEY S3_ACCESS_KEY KAFKA_SASL_PASSWORD KAFKA_SASL_USERNAME; do
      val=${!name:-}
      if [ -n "$val" ]; then
        line=${line//"$val"/[redacted]}
      fi
    done
    printf '%s\n' "$line"
  done < "$1"
}
