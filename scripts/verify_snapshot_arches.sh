#!/usr/bin/env bash
# Runtime-check that the advertised release architectures actually execute.
# A goreleaser cross-build alone proves nothing: the linux/amd64 snapshot
# binary is run natively and the linux/arm64 one under QEMU/binfmt.
#
# Usage: scripts/verify_snapshot_arches.sh [dist-dir]
#
# Expects `goreleaser release --snapshot --clean --skip=docker` output (or an
# equivalent pair of kfuse_*_linux_{amd64,arm64}.tar.gz archives) in dist-dir.
# The arm64 check needs binfmt registered — CI uses docker/setup-qemu-action;
# locally run `docker run --privileged --rm tonistiigi/binfmt --install arm64`.
set -euo pipefail

DIST=${1:-dist}

extract() { # extract <arch>: print the directory holding the binary
  local arch=$1 tarball dir
  tarball=$(find "$DIST" -maxdepth 1 -name "kfuse_*_linux_${arch}.tar.gz" | sort | head -1)
  if [ -z "$tarball" ]; then
    echo "verify-arches: no kfuse_*_linux_${arch}.tar.gz under $DIST" >&2
    exit 1
  fi
  dir=$(mktemp -d "/tmp/kfuse-snap-${arch}.XXXXXX")
  tar -xzf "$tarball" -C "$dir"
  printf '%s\n' "$dir"
}

AMD64_DIR=$(extract amd64)
ARM64_DIR=$(extract arm64)
trap 'rm -rf "$AMD64_DIR" "$ARM64_DIR"; docker rmi kfuse-snap-arm64 >/dev/null 2>&1 || true' EXIT

case $(file -b "$ARM64_DIR/kfuse") in
  *aarch64*|*ARM64*|*"ARM aarch64"*) ;;
  *) echo "verify-arches: arm64 archive does not contain an arm64 binary:" >&2
     file -b "$ARM64_DIR/kfuse" >&2
     exit 1 ;;
esac

AMD64_OUT=$("$AMD64_DIR/kfuse" version)
[ -n "$AMD64_OUT" ] || { echo "verify-arches: linux/amd64 kfuse version printed nothing" >&2; exit 1; }
printf 'linux/amd64 (native): %s\n' "$AMD64_OUT"

# Run the arm64 artifact through binfmt in a scratch container: no base image
# pull needed, and the binary is static (CGO_ENABLED=0).
docker build --platform linux/arm64 -t kfuse-snap-arm64 -f - "$ARM64_DIR" >/dev/null <<'EOF'
FROM scratch
COPY kfuse /kfuse
ENTRYPOINT ["/kfuse"]
EOF

ARM64_OUT=$(docker run --rm --platform linux/arm64 kfuse-snap-arm64 version)
[ -n "$ARM64_OUT" ] || { echo "verify-arches: linux/arm64 kfuse version printed nothing" >&2; exit 1; }
printf 'linux/arm64 (qemu):   %s\n' "$ARM64_OUT"

if [ "$AMD64_OUT" != "$ARM64_OUT" ]; then
  echo "verify-arches: version output differs across architectures" >&2
  exit 1
fi
echo "verify-arches: both advertised architectures execute"
