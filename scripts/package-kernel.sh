#!/usr/bin/env bash
# Empacota Image + módulos compilados em assets prontos para o nanoc.
# Uso: ./scripts/package-kernel.sh [dir do build]   (padrão: .cache/kernel)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${1:-$ROOT/.cache/kernel}"
ASSETS="$ROOT/assets"

[ -f "$SRC/Image" ] || { echo "sem $SRC/Image" >&2; exit 1; }
[ -d "$SRC/mods/lib/modules" ] || { echo "sem módulos em $SRC/mods" >&2; exit 1; }

mkdir -p "$ASSETS"
cp -f "$SRC/Image" "$ASSETS/Image-custom"
rm -f "$ASSETS/modloop-custom"
docker run --rm --platform linux/arm64 -v "$SRC/mods:/mods:ro" -v "$ASSETS:/out" alpine:3.22 sh -c '
  apk add -q squashfs-tools >/dev/null 2>&1
  mksquashfs /mods/lib/modules /out/modloop-custom -comp xz -noappend -quiet
'
ls -lh "$ASSETS/Image-custom" "$ASSETS/modloop-custom"
echo "pronto: use NANOC_KERNEL=custom e NANOC_MODLOOP=modloop-custom"
