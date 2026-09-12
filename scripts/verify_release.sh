#!/usr/bin/env bash
# Verify a release candidate locally without publishing anything:
#
#   - rebuilds the GoReleaser snapshot (archives + checksums; the docker
#     image leg is skipped — cross-arch builds need QEMU/binfmt and are
#     exercised by the release workflow on tag pushes)
#   - validates checksums.txt against the produced archives
#   - checks the advertised linux/amd64 + linux/arm64 archives exist and
#     that the binary inside prints the snapshot version
#   - checks each archive bundles LICENSE, NOTICE, THIRD_PARTY_LICENSES.md
#     and the collected third_party/ license texts
#   - when a Docker daemon is available, builds a native-arch image with
#     Dockerfile.goreleaser and checks the same files inside the image
#
# Run via `make release-verify`.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "==> goreleaser snapshot build"
goreleaser release --snapshot --clean --skip=docker

version=$(python3 -c 'import json; print(json.load(open("dist/metadata.json"))["version"])')
echo "==> snapshot version: $version"

echo "==> validating checksums.txt"
(cd dist && sha256sum --check checksums.txt)

host_arch() {
  case "$(uname -m)" in
    x86_64|amd64)    echo amd64 ;;
    aarch64|arm64)   echo arm64 ;;
    *)               echo "" ;;
  esac
}

can_run() { # $1 = archive arch
  [ "$1" = "$(host_arch)" ] && return 0
  if [ "$1" = arm64 ]; then
    [ -e /proc/sys/fs/binfmt_misc/qemu-aarch64 ] || command -v qemu-aarch64-static >/dev/null 2>&1
    return
  fi
  return 1
}

for arch in amd64 arm64; do
  archive="dist/kfuse_${version}_linux_${arch}.tar.gz"
  echo "==> checking $archive"
  [ -f "$archive" ] || { echo "FAIL: missing $archive"; exit 1; }

  mkdir -p "$WORK/$arch"
  tar -xzf "$archive" -C "$WORK/$arch"
  for f in LICENSE NOTICE THIRD_PARTY_LICENSES.md; do
    [ -s "$WORK/$arch/$f" ] || { echo "FAIL: $archive missing $f"; exit 1; }
  done
  find "$WORK/$arch/third_party" -type f -name 'LICENSE*' | grep -q . \
    || { echo "FAIL: $archive missing third_party license texts"; exit 1; }
  echo "    archive carries LICENSE, NOTICE, THIRD_PARTY_LICENSES.md, third_party/ texts"

  if can_run "$arch"; then
    got=$("$WORK/$arch/kfuse" version)
    [ "$got" = "$version" ] \
      || { echo "FAIL: kfuse version printed '$got', want '$version'"; exit 1; }
    echo "    linux/$arch kfuse version: $got"
  else
    echo "    linux/$arch binary present; version smoke skipped (no qemu-aarch64/binfmt)"
  fi
done

if docker info >/dev/null 2>&1 && [ -n "$(host_arch)" ]; then
  echo "==> checking Dockerfile.goreleaser image carries license files (linux/$(host_arch) only)"
  ctx="$WORK/imgctx"
  mkdir -p "$ctx/linux/$(host_arch)"
  tar -xzf "dist/kfuse_${version}_linux_$(host_arch).tar.gz" -C "$ctx"
  mv "$ctx/kfuse" "$ctx/linux/$(host_arch)/kfuse"
  cp Dockerfile.goreleaser "$ctx/"
  image="kfuse-release-verify:$$"
  docker build --platform "linux/$(host_arch)" -f "$ctx/Dockerfile.goreleaser" -t "$image" "$ctx" >/dev/null
  docker run --rm --entrypoint /bin/sh "$image" -c \
    'set -e; cd /usr/share/doc/kfuse;
     for f in LICENSE NOTICE THIRD_PARTY_LICENSES.md; do [ -s "$f" ]; done;
     find third_party -type f -name "LICENSE*" | grep -q .'
  docker rmi "$image" >/dev/null 2>&1 || true
  echo "    image /usr/share/doc/kfuse carries LICENSE, NOTICE, THIRD_PARTY_LICENSES.md, third_party/ texts"
else
  echo "==> docker daemon unavailable; skipping image content check"
fi

echo "==> release verification passed"
