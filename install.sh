#!/usr/bin/env bash
# Install the kfuse binary.
#
#   curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh | bash -s -- --version v0.1.0 --dest ~/bin
#
# From a checkout:
#
#   ./install.sh                          # download latest prebuilt binary
#   ./install.sh --source                 # build from source instead
#   GOOS=linux GOARCH=amd64 ./install.sh --source --output dist/kfuse
#
# Default mode downloads the matching tar.gz + checksums.txt from GitHub
# Releases for linux/darwin (amd64/arm64) and verifies the sha256 before
# installing. It never falls back to a source build. --source requires a
# kfuse checkout (it needs go.mod and ./cmd/kfuse).
#
# Installs to /usr/local/bin when writable, else $HOME/.local/bin.
set -euo pipefail

REPO=addisonhuddy/kfuse
MODE=release
VERSION=""
DEST=""
OUTPUT=""

die() {
  echo "install.sh: $*" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
Install the kfuse binary.

  curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/addisonhuddy/kfuse/main/install.sh | bash -s -- --version v0.1.0 --dest ~/bin

Options:
  --release            download a prebuilt release binary (default)
  --source             build from source; must run from a kfuse checkout
  --version VERSION    release tag to install, e.g. v0.1.0 (default: latest)
  --dest DIR           install directory (default: /usr/local/bin or ~/.local/bin)
  --output PATH        --source cross-compile output file (with GOOS/GOARCH)
  -h, --help           show this help
USAGE
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
    -h | --help) usage; exit 0 ;;
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
  case "$TARGET_OS" in
    linux | darwin) ;;
    *) die "prebuilt binaries are linux/darwin-only (got $TARGET_OS); use --source" ;;
  esac
  case "$TARGET_ARCH" in
    amd64 | arm64) ;;
    *) die "unsupported architecture $TARGET_ARCH; supported: amd64, arm64" ;;
  esac
  [ -z "$OUTPUT" ] || die "--output is only valid with --source"
  [ "$CROSS" -eq 0 ] ||
    die "release mode installs for the current host only; for another machine download the asset from the releases page or use --source with GOOS/GOARCH and --output"
  missing_tools=()
  for tool in curl tar; do
    command -v "$tool" >/dev/null 2>&1 || missing_tools+=("$tool")
  done
  if command -v sha256sum >/dev/null 2>&1; then
    SHA256_CHECK=(sha256sum)
  elif command -v shasum >/dev/null 2>&1; then
    SHA256_CHECK=(shasum -a 256)
  else
    missing_tools+=("sha256sum or shasum")
  fi
  [ "${#missing_tools[@]}" -eq 0 ] ||
    die "missing required tool(s): ${missing_tools[*]}"
else
  [ -z "$VERSION" ] || die "--version is only valid with --release"
  command -v go >/dev/null 2>&1 || die "required tool not found: go"
  # --source needs the checkout; when the script arrives via a curl pipe there
  # is no script path to locate it from.
  SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" 2>/dev/null && pwd || true)
  [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/go.mod" ] ||
    die "run --source from a kfuse checkout (needs go.mod)"
  cd "$SCRIPT_DIR"
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
  asset="kfuse_${ver}_${TARGET_OS}_${TARGET_ARCH}.tar.gz"
  release_base="${base%/}/$tag"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  echo "install.sh: downloading $asset ($tag)"
  curl -fsSL -o "$tmp/$asset" "$release_base/$asset" ||
    die "release asset $asset not found at $release_base/$asset (check --version and that the release publishes $TARGET_OS/$TARGET_ARCH)"
  curl -fsSL -o "$tmp/checksums.txt" "$release_base/checksums.txt" ||
    die "checksums.txt not found at $release_base/checksums.txt"
  checksum_line=$(grep -E "[[:space:]]$asset$" "$tmp/checksums.txt" || true)
  [ -n "$checksum_line" ] || die "checksums.txt has no entry for $asset"
  printf '%s\n' "$checksum_line" | (cd "$tmp" && "${SHA256_CHECK[@]}" -c - >/dev/null) ||
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
  if [ "$TARGET_OS" = darwin ]; then
    echo "install.sh: macOS needs macFUSE: brew install --cask macfuse"
  fi
fi
