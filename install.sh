#!/usr/bin/env bash
set -euo pipefail

REPO="hxs996-beep/deepAct"
BIN="deepact"

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

# ---- Choose install dir ----
# Prefer /usr/local/bin (already on macOS PATH); fall back to ~/.local/bin
# and auto-add it to the shell rc so `deepact` just works.
INSTALL_MODE="system"
if [ -n "${DEEPACT_INSTALL:-}" ]; then
  INSTALL_DIR="$DEEPACT_INSTALL"
  INSTALL_MODE="custom"
elif [ -w /usr/local/bin ]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="$HOME/.local/bin"
  INSTALL_MODE="user"
fi
echo "📍 Install dir: $INSTALL_DIR"

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
if ! mkdir -p "$INSTALL_DIR"; then
  echo "❌ 无法创建 $INSTALL_DIR（权限不足）"
  echo "   请改用可写目录后重试，例如："
  echo "     DEEPACT_INSTALL=\$HOME/.local/bin $0"
  exit 1
fi

TARGET_BIN="$INSTALL_DIR/$BIN"
if [ -f "$TMP_DIR/$BIN" ]; then
  :
elif [ -f "$TMP_DIR/${BIN}.exe" ]; then
  TARGET_BIN="$INSTALL_DIR/${BIN}.exe"
else
  echo "❌ Binary not found in archive"
  exit 1
fi

if ! mv "$TMP_DIR/$(basename "$TARGET_BIN")" "$TARGET_BIN"; then
  echo "❌ 无法写入 $TARGET_BIN（权限不足）"
  if [ "$INSTALL_MODE" = "system" ]; then
    echo "   macOS 的 /usr/local/bin 通常属 root，普通用户不可写。"
    echo "   推荐安装到用户目录并自动加入 PATH："
    echo "     DEEPACT_INSTALL=\$HOME/.local/bin $0"
    echo "   （不指定时脚本会自动回退到 ~/.local/bin 并写入 shell 配置）"
  fi
  exit 1
fi
chmod +x "$TARGET_BIN"

# ---- Ensure install dir is on PATH (user-mode installs only) ----
if [ "$INSTALL_MODE" = "user" ]; then
  if ! printf ':%s:' "$PATH" | grep -qF ":$INSTALL_DIR:"; then
    # Use a $HOME-relative form in the rc file so it survives username changes
    RC_DIR="$INSTALL_DIR"
    case "$INSTALL_DIR" in
      "$HOME"/*) RC_DIR='$HOME'"${INSTALL_DIR#$HOME}" ;;
    esac
    RC_FILE=""
    if [ -f "$HOME/.zshrc" ]; then
      RC_FILE="$HOME/.zshrc"
    elif [ -f "$HOME/.bashrc" ]; then
      RC_FILE="$HOME/.bashrc"
    elif [ -f "$HOME/.bash_profile" ]; then
      RC_FILE="$HOME/.bash_profile"
    else
      RC_FILE="$HOME/.zshrc"
    fi
    if ! grep -qF "$RC_DIR" "$RC_FILE" 2>/dev/null; then
      {
        echo ""
        echo "# added by deepact installer"
        echo "if [ -d $RC_DIR ]; then"
        echo "  export PATH=\"$RC_DIR:\$PATH\""
        echo "fi"
      } >> "$RC_FILE"
      echo "🔧 Added $INSTALL_DIR to PATH in $RC_FILE"
      echo "   Run: source $RC_FILE   (or open a new terminal)"
    fi
  fi
fi

echo "✅ Installed $BIN $LATEST to $TARGET_BIN"
echo ""
echo "   Run:  deepact"
echo "   Help: deepact --help"
