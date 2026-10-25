#!/usr/bin/env sh
set -eu

REPO="kuarezma/lazylocal"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

case "$OS" in
  darwin|linux) ;;
  *)
    echo "Unsupported OS: $OS (supported: darwin, linux)" >&2
    exit 1
    ;;
esac

BINARY="lazylocal-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${BINARY}"

mkdir -p "$INSTALL_DIR"
TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT

echo "⚡ Downloading lazylocal (${OS}/${ARCH}) from ${URL}..."
curl -fsSL "$URL" -o "$TMP_FILE"
chmod +x "$TMP_FILE"
mv "$TMP_FILE" "${INSTALL_DIR}/lazylocal"

echo "✅ Installed to ${INSTALL_DIR}/lazylocal"
"${INSTALL_DIR}/lazylocal" --version || true
