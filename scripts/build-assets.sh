#!/usr/bin/env bash
# Gera dist/nanoc-assets-<versão>.tar.gz com kernel, initramfs, modloop e rootfs.
# Roda em Linux com Docker (usado pelo CI para anexar os assets ao release).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="${1:-dev}"
export NANOC_STATE="$ROOT/dist"

rm -rf dist && mkdir -p dist/assets
./scripts/bootstrap.sh

DIST="dist/nanoc-assets-${VERSION}.tar.gz"
tar -czf "$DIST" -C dist assets/Image assets/initramfs.cpio.gz assets/modloop-virt rootfs.sqfs
ls -lh "$DIST"
