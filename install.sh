#!/usr/bin/env sh
# cix installer — downloads the latest release binary.
#   curl -fsSL https://raw.githubusercontent.com/Dvorinka/cix/main/install.sh | sh
# Env: CIX_DIR (default ~/.local/bin), CIX_VERSION (default latest)
set -eu

REPO="Dvorinka/cix"
TOOL="cix"
DIR="${CIX_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in linux|darwin) ;; *) echo "unsupported os: $os (use go install)" >&2; exit 1 ;; esac

VER_VAR="CIX_VERSION"
VER=$(eval "echo \$$VER_VAR")
if [ -z "$VER" ]; then
  VER=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
fi
[ -z "$VER" ] && { echo "no release found — install with: go install github.com/$REPO/cmd/$TOOL@latest" >&2; exit 1; }

url="https://github.com/$REPO/releases/download/$VER/$TOOL-$VER-$os-$arch"
mkdir -p "$DIR"
curl -fsSL "$url" -o "$DIR/$TOOL"
chmod +x "$DIR/$TOOL"
echo "$TOOL $VER -> $DIR/$TOOL"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "add $DIR to PATH" >&2 ;; esac
