#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
server_pid=
failures=0
trap 'if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; fi; rm -rf "$tmp"' EXIT

srv="$tmp/server"
mkdir -p "$srv/download/v9.9.9" "$srv/api"
cat > "$tmp/kfuse" <<'EOF'
#!/bin/sh
echo "kfuse v9.9.9-fake"
EOF
chmod 0755 "$tmp/kfuse"
tar -czf "$srv/download/v9.9.9/kfuse_9.9.9_linux_amd64.tar.gz" -C "$tmp" kfuse
tar -czf "$srv/download/v9.9.9/kfuse_9.9.9_darwin_amd64.tar.gz" -C "$tmp" kfuse
(cd "$srv/download/v9.9.9" && sha256sum kfuse_9.9.9_linux_amd64.tar.gz kfuse_9.9.9_darwin_amd64.tar.gz > checksums.txt)
printf '%s\n' '{"tag_name":"v9.9.9"}' > "$srv/api/latest"

port=$(python3 - <<'PY'
import socket

with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
python3 -m http.server "$port" --directory "$srv" > "$tmp/http.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 50); do
  if python3 - "$port" <<'PY'
import socket
import sys

with socket.socket() as sock:
    sock.settimeout(0.1)
    try:
        sock.connect(("127.0.0.1", int(sys.argv[1])))
    except OSError:
        raise SystemExit(1)
PY
  then
    break
  fi
  sleep 0.1
done

export KFUSE_RELEASE_BASE_URL="http://127.0.0.1:$port/download"
export KFUSE_RELEASE_API_URL="http://127.0.0.1:$port/api/latest"
non_host_arch=arm64
[ "$(uname -m)" = arm64 ] && non_host_arch=amd64

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

release_env=(env GOARCH=amd64 GOOS=linux)
run_case release_latest_ok 0 "${release_env[@]}" bash "$ROOT/install.sh" --release --dest "$tmp/bin"
[ -x "$tmp/bin/kfuse" ] || { echo "FAIL release_latest_ok (binary missing)"; failures=$((failures + 1)); }
assert_output release_latest_ok "installed kfuse"
assert_output release_latest_ok "$tmp/bin"
assert_output release_latest_ok "not on your PATH"

run_case default_mode_release_ok 0 "${release_env[@]}" bash "$ROOT/install.sh" --dest "$tmp/default-bin"
[ -x "$tmp/default-bin/kfuse" ] || { echo "FAIL default_mode_release_ok (binary missing)"; failures=$((failures + 1)); }
assert_output default_mode_release_ok "installed kfuse"

run_case pipe_help 0 bash -c "cat '$ROOT/install.sh' | bash -s -- --help"
assert_output pipe_help "curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh"

# Fake a darwin host: uname -s/-m come from a stub on PATH, no GOOS/GOARCH.
fakedir="$tmp/fake-darwin"
mkdir -p "$fakedir"
cat > "$fakedir/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) echo Darwin ;;
  -m) echo x86_64 ;;
  *) echo Darwin ;;
esac
EOF
chmod 0755 "$fakedir/uname"
run_case release_darwin_ok 0 env -u GOOS -u GOARCH PATH="$fakedir:$PATH" \
  bash "$ROOT/install.sh" --version v9.9.9 --dest "$tmp/darwin-bin"
[ -x "$tmp/darwin-bin/kfuse" ] || { echo "FAIL release_darwin_ok (binary missing)"; failures=$((failures + 1)); }
assert_output release_darwin_ok "kfuse_9.9.9_darwin_amd64.tar.gz"
assert_output release_darwin_ok "macFUSE"

run_case release_version_ok 0 "${release_env[@]}" bash "$ROOT/install.sh" --release --version v9.9.9 --dest "$tmp/version-bin"
[ -x "$tmp/version-bin/kfuse" ] || { echo "FAIL release_version_ok (binary missing)"; failures=$((failures + 1)); }

run_case release_missing_asset 1 "${release_env[@]}" bash "$ROOT/install.sh" --release --version v0.0.1 --dest "$tmp/missing-bin"
assert_output release_missing_asset "not found"
if grep -Fq "building kfuse" "$tmp/release_missing_asset.out" || [ -e "$tmp/missing-bin/kfuse" ]; then
  echo "FAIL release_missing_asset (fallback or binary installed)"
  failures=$((failures + 1))
fi

cp "$srv/download/v9.9.9/checksums.txt" "$tmp/checksums.good"
printf '%064d  kfuse_9.9.9_linux_amd64.tar.gz\n' 0 > "$srv/download/v9.9.9/checksums.txt"
run_case release_checksum_mismatch 1 "${release_env[@]}" bash "$ROOT/install.sh" --release --version v9.9.9 --dest "$tmp/checksum-bin"
assert_output release_checksum_mismatch "checksum mismatch"
[ ! -e "$tmp/checksum-bin/kfuse" ] || { echo "FAIL release_checksum_mismatch (binary installed)"; failures=$((failures + 1)); }
cp "$tmp/checksums.good" "$srv/download/v9.9.9/checksums.txt"

run_case release_unsupported_os 1 env GOARCH=amd64 GOOS=windows bash "$ROOT/install.sh" --release --dest "$tmp/os-bin"
assert_output release_unsupported_os "linux/darwin-only"
[ ! -d "$tmp/os-bin" ] || { echo "FAIL release_unsupported_os (dest created)"; failures=$((failures + 1)); }

run_case release_unsupported_arch 1 env GOARCH=mips GOOS=linux bash "$ROOT/install.sh" --release --dest "$tmp/arch-bin"
assert_output release_unsupported_arch "unsupported architecture"
assert_output release_unsupported_arch "supported: amd64, arm64"

run_case release_cross_rejected 1 env GOARCH="$non_host_arch" GOOS=linux bash "$ROOT/install.sh" --release --dest "$tmp/cross-bin"
assert_output release_cross_rejected "--source"

minimal_path="$tmp/minimal-path"
mkdir -p "$minimal_path"
for tool in bash uname mkdir tar sha256sum grep sed head install mktemp rm tr cat env dirname chmod; do
  ln -s "$(command -v "$tool")" "$minimal_path/$tool"
done
run_case release_missing_curl 1 env PATH="$minimal_path" GOARCH=amd64 GOOS=linux HOME="$HOME" \
  /bin/bash "$ROOT/install.sh" --release --dest "$tmp/no-curl-bin"
assert_output release_missing_curl "curl"

if command -v go >/dev/null 2>&1; then
  run_case source_cross_output 0 env GOARCH="$non_host_arch" GOOS=linux \
    bash "$ROOT/install.sh" --source --dest "$tmp/source-bin" --output "$tmp/out/kfuse-x"
  [ -x "$tmp/out/kfuse-x" ] || { echo "FAIL source_cross_output (artifact missing)"; failures=$((failures + 1)); }
  assert_output source_cross_output "not installed on this host"
  [ ! -e "$tmp/source-bin/kfuse" ] || { echo "FAIL source_cross_output (native binary created)"; failures=$((failures + 1)); }
else
  echo "skip source_cross_output (go unavailable)"
fi

run_case source_version_flag_rejected 1 env GOARCH=amd64 GOOS=linux \
  bash "$ROOT/install.sh" --source --version v1 --dest "$tmp/source-version-bin"
assert_output source_version_flag_rejected "only valid with --release"

if [ "$failures" -ne 0 ]; then
  echo "$failures installer test(s) failed" >&2
  exit 1
fi
