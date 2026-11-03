#!/usr/bin/env bash
# Gera dist/nanoc-assets-<versão>.tar.gz com kernel, initramfs, modloop e rootfs.
# Roda em Linux com Docker (usado pelo CI para anexar os assets ao release).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="${1:-dev}"
export NANOC_STATE="$ROOT/dist"
export PLATFORM="${PLATFORM:-linux/arm64}"
export ALPINE_CHANNEL="${ALPINE_CHANNEL:-edge}"

rm -rf dist && mkdir -p dist/assets
./scripts/bootstrap.sh

DIST="dist/nanoc-assets-${VERSION}.tar.gz"
EXTRA=""; [ -f dist/assets/.modloop-notify ] && EXTRA=assets/.modloop-notify
tar -czf "$DIST" -C dist assets/Image assets/initramfs.cpio.gz assets/modloop-virt $EXTRA rootfs.erofs rootfs.sqfs
ls -lh "$DIST"
