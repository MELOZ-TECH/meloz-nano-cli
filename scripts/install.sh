#!/usr/bin/env bash
# Instala o nanoc a partir de um release do GitHub (macOS arm64).
set -euo pipefail

REPO="MELOZ-TECH/nano-container"
VERSION="${1:-latest}"
PREFIX="${PREFIX:-/usr/local/bin}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "este instalador suporta apenas macOS arm64" >&2
  exit 1
fi

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
fi

URL="https://github.com/$REPO/releases/download/$VERSION/nanoc-$VERSION-darwin-arm64.tar.gz"
echo "baixando $URL"
curl -fsSL -o "$TMP/nanoc.tar.gz" "$URL"
tar -xzf "$TMP/nanoc.tar.gz" -C "$TMP"

mkdir -p "$PREFIX"
install -m 0755 "$TMP/nanoc" "$PREFIX/nanoc"
ENT="$TMP/build/virt.entitlements"
[ -f "$ENT" ] || ENT="$TMP/virt.entitlements"
if [ -f "$ENT" ]; then
  codesign --force --sign - --entitlements "$ENT" "$PREFIX/nanoc"
else
  echo "aviso: virt.entitlements não encontrado no pacote; assine manualmente" >&2
fi

echo "nanoc instalado em $PREFIX/nanoc ($VERSION)"
echo
echo "próximos passos:"
echo "  1. nanoc bootstrap   # baixa kernel/rootfs do release"
echo "  2. nanoc up"
echo "  3. export DOCKER_HOST=unix://\$HOME/.nanoc/docker.sock"
