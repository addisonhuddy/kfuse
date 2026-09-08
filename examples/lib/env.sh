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

kfuse_apply_defaults() {
  [ -n "${KAFKA_TLS:-}" ] || export KAFKA_TLS=true
  [ -n "${KAFKA_TOPIC:-}" ] || export KAFKA_TOPIC=kfuse.events
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
  kfuse_apply_defaults
  kfuse_require_env \
    BOOTSTRAP_SERVER S3_ACCESS_KEY S3_SECRET_KEY S3_BUCKET
}
