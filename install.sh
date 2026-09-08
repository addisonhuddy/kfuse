#!/usr/bin/env bash
# Install the kfuse binary.
#
#   ./install.sh                          # build from source, native GOOS/GOARCH
#   GOOS=linux GOARCH=amd64 ./install.sh  # cross-compile from source for a sandbox
#   ./install.sh --release                # download latest prebuilt linux binary
#   ./install.sh --release --version v0.1.0
#   ./install.sh --source                 # force from-source even if --release fails
#
# --release fetches the matching tar.gz + checksums.txt from GitHub Releases,
# verifies the sha256, and falls back to a from-source build if anything is
# missing (no curl, no matching asset, or no network).
#
# Installs to /usr/local/bin when writable, else $HOME/.local/bin.
set -euo pipefail
cd "$(dirname "$0")"

REPO=addisonhuddy/kfuse
MODE=source
VERSION=""

while [ $# -gt 0 ]; do
  case "$1" in
    --release) MODE=release ;;
    --source) MODE=source ;;
    --version) VERSION="$2"; shift ;;
    --version=*) VERSION="${1#--version=}" ;;
    -h | --help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "install.sh: unknown flag $1" >&2; exit 2 ;;
  esac
  shift
done

if [ -z "${GOARCH:-}" ]; then
  case "$(uname -m)" in
    x86_64) GOARCH=amd64 ;;
    aarch64 | arm64) GOARCH=arm64 ;;
    *) GOARCH="$(uname -m)" ;;
  esac
fi
GOOS="${GOOS:-}"

if [ -w /usr/local/bin ] 2>/dev/null; then
  DEST=/usr/local/bin
else
  DEST="$HOME/.local/bin"
fi
mkdir -p "$DEST"

build_from_source() {
  echo "install.sh: building kfuse (GOOS=${GOOS:-<native>} GOARCH=$GOARCH)"
  CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" \
    -o "$DEST/kfuse" ./cmd/kfuse
  echo "install.sh: installed -> $DEST/kfuse"
}

download_release() {
  local os="${GOOS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
  if [ "$os" != linux ]; then
    echo "install.sh: prebuilt binaries are linux-only (got $os)" >&2
    return 1
  fi
  command -v curl >/dev/null || { echo "install.sh: curl not found" >&2; return 1; }

  local tag="$VERSION"
  if [ -z "$tag" ]; then
    tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
    [ -n "$tag" ] || { echo "install.sh: could not resolve latest release" >&2; return 1; }
  fi
  local ver="${tag#v}"
  local asset="kfuse_${ver}_linux_${GOARCH}.tar.gz"
  local base="https://github.com/$REPO/releases/download/$tag"

  local tmp
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN

  echo "install.sh: downloading $asset ($tag)"
  curl -fsSL -o "$tmp/$asset" "$base/$asset" || return 1
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || return 1
  (cd "$tmp" && grep " $asset\$" checksums.txt | sha256sum -c --quiet -) \
    || { echo "install.sh: checksum mismatch for $asset" >&2; return 1; }
  tar -xzf "$tmp/$asset" -C "$tmp" kfuse
  install -m 0755 "$tmp/kfuse" "$DEST/kfuse"
  echo "install.sh: installed $tag -> $DEST/kfuse"
}

if [ "$MODE" = release ]; then
  if download_release; then
    exit 0
  fi
  echo "install.sh: release download failed; falling back to source build" >&2
fi
build_from_source
