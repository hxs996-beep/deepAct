#!/usr/bin/env bash
set -euo pipefail

REPO="deepact/deepact"
BIN="deepact"
INSTALL_DIR="${DEEPACT_INSTALL:-/usr/local/bin}"

# ---- Platform detection ----
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "❌ Unsupported architecture: $ARCH"; exit 1 ;;
esac
case "$OS" in
  darwin|linux) ;;
  mingw*|msys*|cygwin*) OS="windows" ;;
  *) echo "❌ Unsupported OS: $OS"; exit 1 ;;
esac

# ---- Get latest version from GitHub ----
echo "📡 Looking up latest release..."
LATEST="$(curl -sSfL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
if [ -z "$LATEST" ]; then
  echo "❌ Failed to get latest version"
  exit 1
fi
echo "   Latest: $LATEST"

# ---- Download ----
ARCHIVE_NAME="${BIN}_${LATEST}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST/$ARCHIVE_NAME"
echo "📥 Downloading $ARCHIVE_NAME ..."
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
curl -sSfL "$DOWNLOAD_URL" -o "$TMP_DIR/$ARCHIVE_NAME"

# ---- Extract ----
echo "📦 Extracting..."
tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"

# ---- Install ----
echo "🔧 Installing to $INSTALL_DIR/$BIN ..."
mkdir -p "$INSTALL_DIR"
if [ -f "$TMP_DIR/$BIN" ]; then
  mv "$TMP_DIR/$BIN" "$INSTALL_DIR/$BIN"
elif [ -f "$TMP_DIR/${BIN}.exe" ]; then
  mv "$TMP_DIR/${BIN}.exe" "$INSTALL_DIR/${BIN}.exe"
fi
chmod +x "$INSTALL_DIR/$BIN"

echo "✅ Installed $BIN $LATEST to $INSTALL_DIR/$BIN"
echo ""
echo "   Run:  deepact"
echo "   Help: deepact --help"
