#!/usr/bin/env bash
# Install the kfuse binary.
#
#   ./install.sh                          # build from source, native GOOS/GOARCH
#   ./install.sh --release                # download latest prebuilt linux binary
#   ./install.sh --release --version v0.1.0
#   ./install.sh --release --dest DIR
#   GOOS=linux GOARCH=amd64 ./install.sh --source --output dist/kfuse
#
# --release fetches the matching tar.gz + checksums.txt from GitHub Releases
# and verifies the sha256 before installing. It never falls back to a source
# build.
#
# Installs to /usr/local/bin when writable, else $HOME/.local/bin.
set -euo pipefail
cd "$(dirname "$0")"

REPO=addisonhuddy/kfuse
MODE=source
VERSION=""
DEST=""
OUTPUT=""

die() {
  echo "install.sh: $*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --release) MODE=release ;;
    --source) MODE=source ;;
    --version)
      [ $# -ge 2 ] || { echo "install.sh: --version requires a value" >&2; exit 2; }
      VERSION="$2"
      shift
      ;;
    --version=*) VERSION="${1#--version=}" ;;
    --dest)
      [ $# -ge 2 ] || { echo "install.sh: --dest requires a value" >&2; exit 2; }
      DEST="$2"
      shift
      ;;
    --dest=*) DEST="${1#--dest=}" ;;
    --output)
      [ $# -ge 2 ] || { echo "install.sh: --output requires a value" >&2; exit 2; }
      OUTPUT="$2"
      shift
      ;;
    --output=*) OUTPUT="${1#--output=}" ;;
    -h | --help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "install.sh: unknown flag $1" >&2; exit 2 ;;
  esac
  shift
done

HOST_OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64) HOST_ARCH=amd64 ;;
  aarch64 | arm64) HOST_ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m); supported: amd64, arm64" ;;
esac
TARGET_OS=${GOOS:-$HOST_OS}
TARGET_ARCH=${GOARCH:-$HOST_ARCH}
CROSS=0
if [ "$TARGET_OS" != "$HOST_OS" ] || [ "$TARGET_ARCH" != "$HOST_ARCH" ]; then
  CROSS=1
fi

if [ "$MODE" = release ]; then
  [ "$TARGET_OS" = linux ] ||
    die "prebuilt binaries are linux-only (got $TARGET_OS); use --source"
  case "$TARGET_ARCH" in
    amd64 | arm64) ;;
    *) die "unsupported architecture $TARGET_ARCH; supported: amd64, arm64" ;;
  esac
  [ -z "$OUTPUT" ] || die "--output is only valid with --source"
  [ "$CROSS" -eq 0 ] ||
    die "release mode installs for the current host only; for another machine download the asset from the releases page or use --source with GOOS/GOARCH and --output"
  missing_tools=()
  for tool in curl tar sha256sum; do
    command -v "$tool" >/dev/null 2>&1 || missing_tools+=("$tool")
  done
  [ "${#missing_tools[@]}" -eq 0 ] ||
    die "missing required tool(s): ${missing_tools[*]}"
else
  [ -z "$VERSION" ] || die "--version is only valid with --release"
  command -v go >/dev/null 2>&1 || die "required tool not found: go"
  if [ "$CROSS" -eq 0 ] && [ -n "$OUTPUT" ]; then
    echo "install.sh: --output is only valid for cross-compilation" >&2
    exit 2
  fi
fi

if [ "$CROSS" -eq 1 ]; then
  OUTPUT=${OUTPUT:-"./dist/kfuse_${TARGET_OS}_${TARGET_ARCH}"}
else
  if [ -z "$DEST" ]; then
    if [ -w /usr/local/bin ] 2>/dev/null; then
      DEST=/usr/local/bin
    else
      DEST="${HOME}/.local/bin"
    fi
  fi
  mkdir -p "$DEST" || die "could not create destination directory $DEST"
fi

installed_version() {
  local version
  version=$("$DEST/kfuse" version 2>/dev/null || true)
  if [ -z "$version" ]; then
    version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
  fi
  echo "$version"
}

build_from_source() {
  local destination="$DEST/kfuse"
  if [ "$CROSS" -eq 1 ]; then
    destination="$OUTPUT"
    mkdir -p "$(dirname "$destination")" || die "could not create output directory for $destination"
  fi
  echo "install.sh: building kfuse (GOOS=$TARGET_OS GOARCH=$TARGET_ARCH)"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" go build \
    -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" \
    -o "$destination" ./cmd/kfuse ||
    die "source build failed"
  if [ "$CROSS" -eq 1 ]; then
    echo "install.sh: built cross-compiled artifact -> $destination (GOOS=$TARGET_OS GOARCH=$TARGET_ARCH); it is not installed on this host — copy it to the target machine"
  fi
}

download_release() {
  local tag="$VERSION"
  local ver asset release_base tmp checksum_line
  local base="${KFUSE_RELEASE_BASE_URL:-https://github.com/$REPO/releases/download}"
  local api_url="${KFUSE_RELEASE_API_URL:-https://api.github.com/repos/$REPO/releases/latest}"

  if [ -z "$tag" ]; then
    tag=$(curl -fsSL "$api_url" |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1) ||
      die "could not resolve latest release from $api_url"
    [ -n "$tag" ] || die "could not resolve latest release from $api_url"
  fi
  ver="${tag#v}"
  asset="kfuse_${ver}_linux_${TARGET_ARCH}.tar.gz"
  release_base="${base%/}/$tag"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  echo "install.sh: downloading $asset ($tag)"
  curl -fsSL -o "$tmp/$asset" "$release_base/$asset" ||
    die "release asset $asset not found at $release_base/$asset (check --version and that the release publishes linux/$TARGET_ARCH)"
  curl -fsSL -o "$tmp/checksums.txt" "$release_base/checksums.txt" ||
    die "checksums.txt not found at $release_base/checksums.txt"
  checksum_line=$(grep -E "[[:space:]]$asset$" "$tmp/checksums.txt" || true)
  [ -n "$checksum_line" ] || die "checksums.txt has no entry for $asset"
  printf '%s\n' "$checksum_line" | (cd "$tmp" && sha256sum -c --quiet -) ||
    die "checksum mismatch for $asset; refusing to install"
  tar -tzf "$tmp/$asset" | grep -qx 'kfuse' ||
    die "release archive $asset does not contain kfuse"
  tar -xzf "$tmp/$asset" -C "$tmp" kfuse ||
    die "could not extract kfuse from $asset"
  install -m 0755 "$tmp/kfuse" "$DEST/kfuse" ||
    die "could not install kfuse to $DEST/kfuse"
}

if [ "$MODE" = release ]; then
  download_release
else
  build_from_source
fi

if [ "$CROSS" -eq 0 ]; then
  echo "install.sh: installed kfuse $(installed_version) -> $DEST/kfuse"
  case ":$PATH:" in
    *":$DEST:"*) ;;
    *) echo "install.sh: $DEST is not on your PATH; add it with: export PATH=\"$DEST:\$PATH\"" >&2 ;;
  esac
fi
