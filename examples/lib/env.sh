#!/usr/bin/env bash
# Shared kfuse example environment loader.

kfuse_load_env() {
  local repo_root file line key value first last
  repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
  file=${1:-${KFUSE_ENV_FILE:-$repo_root/.env}}
  [ -f "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [ -n "$line" ] || continue
    [ "${line:0:1}" = "#" ] && continue
    case "$line" in
      export\ *) line=${line#export } ;;
    esac
    case "$line" in
      *=*) key=${line%%=*}; value=${line#*=} ;;
      *) continue ;;
    esac
    key="${key%"${key##*[![:space:]]}"}"
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    if [ "${#value}" -ge 2 ]; then
      first=${value:0:1}
      last=${value: -1}
      if { [ "$first" = "'" ] && [ "$last" = "'" ]; } ||
         { [ "$first" = '"' ] && [ "$last" = '"' ]; }; then
        value=${value:1:${#value}-2}
      fi
    fi
    [ -n "${!key:-}" ] || export "$key=$value"
  done < "$file"
}

kfuse_resolve_aliases() {
  local canonical short pair
  local aliases=(
    "KF_KAFKA_BROKERS BOOTSTRAP_SERVER"
    "KF_KAFKA_SASL_USERNAME CONFLUENT_CLOUD_KEY"
    "KF_KAFKA_SASL_PASSWORD CONFLUENT_CLOUD_SECRET"
    "AWS_REGION REGION"
    "AWS_ACCESS_KEY_ID AWS_ACCESS_KEY"
    "AWS_SECRET_ACCESS_KEY AWS_SECRET_KEY"
    "KF_BLOB_BUCKET BUCKET"
  )
  for pair in "${aliases[@]}"; do
    canonical=${pair%% *}
    short=${pair#* }
    if [ -z "${!canonical:-}" ] && [ -n "${!short:-}" ]; then
      export "$canonical=${!short}"
    fi
  done
  [ -n "${KF_KAFKA_TLS:-}" ] || export KF_KAFKA_TLS=true
  [ -n "${KF_KAFKA_TOPIC:-}" ] || export KF_KAFKA_TOPIC=kfuse.events
}

kfuse_require_env() {
  local name caller=bash joined="" missing=() index
  for ((index = 1; index < ${#BASH_SOURCE[@]}; index++)); do
    case "${BASH_SOURCE[index]}" in
      */env.sh|env.sh) ;;
      *) caller=${BASH_SOURCE[index]}; break ;;
    esac
  done
  caller=${caller##*/}
  caller=${caller:-bash}
  for name in "$@"; do
    [ -n "${!name:-}" ] || missing+=("$name")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    for name in "${missing[@]}"; do
      [ -n "$joined" ] && joined+=", "
      joined+=$name
    done
    echo "$caller: missing required env: $joined (set them in .env or the environment)" >&2
    return 1
  fi
}

kfuse_env_init() {
  kfuse_load_env "${1:-}"
  kfuse_resolve_aliases
  kfuse_require_env \
    KF_KAFKA_BROKERS KF_KAFKA_SASL_USERNAME KF_KAFKA_SASL_PASSWORD \
    AWS_REGION AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY KF_BLOB_BUCKET
}
