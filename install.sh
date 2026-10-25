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

VERSION="${VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  BASE_URL="https://github.com/${REPO}/releases/latest/download"
else
  BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
fi

BINARY="lazylocal-${OS}-${ARCH}"
URL="${BASE_URL}/${BINARY}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

mkdir -p "$INSTALL_DIR"
TMP_FILE="$(mktemp)"
TMP_CHECKSUMS="$(mktemp)"
trap 'rm -f "$TMP_FILE" "$TMP_CHECKSUMS"' EXIT

echo "⚡ Downloading lazylocal (${OS}/${ARCH}) from ${URL}..."
curl -fsSL "$URL" -o "$TMP_FILE"

echo "🔒 Verifying SHA-256 checksum..."
if curl -fsSL "$CHECKSUMS_URL" -o "$TMP_CHECKSUMS" 2>/dev/null; then
  EXPECTED_HASH="$(grep "${BINARY}" "$TMP_CHECKSUMS" | awk '{print $1}')"
  if [ -z "$EXPECTED_HASH" ]; then
    echo "❌ Error: Could not find checksum for ${BINARY} in checksums.txt" >&2
    rm -f "$TMP_FILE"
    exit 1
  fi

  ACTUAL_HASH=""
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_HASH="$(sha256sum "$TMP_FILE" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_HASH="$(shasum -a 256 "$TMP_FILE" | awk '{print $1}')"
  else
    echo "❌ Error: Neither sha256sum nor shasum found for cryptographic verification" >&2
    rm -f "$TMP_FILE"
    exit 1
  fi

  if [ "$EXPECTED_HASH" != "$ACTUAL_HASH" ]; then
    echo "❌ Security Alert: SHA-256 Checksum verification failed!" >&2
    echo "  Expected: $EXPECTED_HASH" >&2
    echo "  Actual:   $ACTUAL_HASH" >&2
    rm -f "$TMP_FILE"
    exit 1
  fi
  echo "✅ Checksum verified: ${ACTUAL_HASH:0:16}..."
else
  echo "⚠️  Warning: checksums.txt not available on release; proceeding." >&2
fi

chmod +x "$TMP_FILE"
mv "$TMP_FILE" "${INSTALL_DIR}/lazylocal"

echo "✅ Installed to ${INSTALL_DIR}/lazylocal"
"${INSTALL_DIR}/lazylocal" --version || true
