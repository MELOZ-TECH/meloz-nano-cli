#!/usr/bin/env bash
# Baixa e monta os artefatos do guest (kernel, initramfs, modloop, rootfs, data.img).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALPINE_VER="${ALPINE_VER:-3.22}"
ALPINE_CHANNEL="${ALPINE_CHANNEL:-edge}"
ARCH="${ARCH:-aarch64}"
if [ "$ALPINE_CHANNEL" = "edge" ]; then
  BASE="https://dl-cdn.alpinelinux.org/alpine/edge/releases/${ARCH}/netboot"
else
  BASE="https://dl-cdn.alpinelinux.org/alpine/v${ALPINE_VER}/releases/${ARCH}/netboot"
fi
ASSETS="$ROOT/assets"
CACHE="$ROOT/.cache"
STATE="${NANOC_STATE:-$HOME/.nanoc}"

mkdir -p "$ASSETS" "$CACHE"

fetch() { # url dest
  [ -f "$2" ] || curl -fsSL --retry 3 -o "$2" "$1"
  echo "ok $(basename "$2")"
}

fetch "$BASE/vmlinuz-virt" "$ASSETS/vmlinuz-virt"
fetch "$BASE/initramfs-virt" "$ASSETS/initramfs-virt"
fetch "$BASE/modloop-virt" "$ASSETS/modloop-virt"

# módulo nanoc_notify (avisos de arquivo do Mac para os containers no backend
# libkrun) dentro do próprio modloop. Opcional: sem ele o daemon só desliga os
# avisos. O marcador guarda o sha256 do modloop já empacotado.
if [ "${NANOC_NOTIFY_MODULE:-1}" != 0 ] && \
   [ "$(shasum -a 256 "$ASSETS/modloop-virt" | cut -d' ' -f1)" != "$(cat "$ASSETS/.modloop-notify" 2>/dev/null)" ]; then
  if "$ROOT/guest/notify/build.sh" "$ASSETS/modloop-virt" &&
     "$ROOT/guest/notify/pack-modloop.sh" "$ASSETS/modloop-virt" "$ASSETS/modloop-virt.notify"; then
    mv "$ASSETS/modloop-virt.notify" "$ASSETS/modloop-virt"
    shasum -a 256 "$ASSETS/modloop-virt" | cut -d' ' -f1 > "$ASSETS/.modloop-notify"
  else
    echo "aviso: modloop sem nanoc_notify; os containers no libkrun ficam sem avisos de arquivo"
  fi
fi

# kernel EFI zboot -> Image arm64 cru
if [ ! -f "$ASSETS/Image" ]; then
  python3 - "$ASSETS/vmlinuz-virt" "$ASSETS/Image" <<'PY'
import sys, zlib
src, dst = sys.argv[1], sys.argv[2]
data = open(src, "rb").read()
i = data.find(b"\x1f\x8b\x08")
if i < 0:
    raise SystemExit("kernel comprimido não encontrado")
img = zlib.decompressobj(16 + zlib.MAX_WBITS).decompress(data[i:])
if img[0x38:0x3C] != b"ARMd":
    raise SystemExit("payload não é Image arm64")
open(dst, "wb").write(img)
PY
  echo "ok Image"
fi

# rootfs Alpine + Docker (via container)
if [ ! -d "$ROOT/rootfs" ]; then
  docker build ${PLATFORM:+--platform "$PLATFORM"} -q -t nanoc-rootfs "$ROOT/guest" >/dev/null
  cid=$(docker create nanoc-rootfs)
  mkdir -p "$ROOT/rootfs"
  docker export "$cid" | tar -x -C "$ROOT/rootfs"
  docker rm "$cid" >/dev/null
  echo "ok rootfs"
fi

# binários do guest (estáticos, linux/arm64)
( cd "$ROOT/guestinit" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='-s -w' -o "$ROOT/rootfs/guestinit" . )
( cd "$ROOT/agent" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='-s -w' -o "$ROOT/rootfs/opt/nanoc-agent" . )
cp "$ROOT/guest/nanoc-guest.sh" "$ROOT/rootfs/opt/nanoc-guest.sh"
chmod +x "$ROOT/rootfs/opt/nanoc-guest.sh" "$ROOT/rootfs/opt/nanoc-agent" "$ROOT/rootfs/guestinit"

# initramfs do Alpine + o init próprio. Os demais módulos vêm do modloop (vda),
# que o guestinit monta em /lib/modules; copiá-los aqui deixava 70 MB a mais na RAM.
rm -rf "$CACHE/initrd" && mkdir -p "$CACHE/initrd"
gzip -dc "$ASSETS/initramfs-virt" | ( cd "$CACHE/initrd" && cpio -idm --quiet )
cp "$ROOT/rootfs/guestinit" "$CACHE/initrd/guestinit"
chmod +x "$CACHE/initrd/guestinit"
( cd "$CACHE/initrd" && find . | cpio -o -H newc --quiet | gzip -9 ) > "$ASSETS/initramfs.cpio.gz"
echo "ok initramfs"

# rootfs comprimido read-only (squashfs; o kernel Alpine virt não tem EROFS)
if [ ! -f "$STATE/rootfs.sqfs" ] || [ -n "${FORCE_ROOTFS:-}" ]; then
  docker run --rm --privileged -v "$ROOT/rootfs:/rootfs:ro" -v "$STATE:/out" alpine:3.22 sh -c \
    'apk add -q squashfs-tools >/dev/null 2>&1; rm -f /out/rootfs.sqfs; mksquashfs /rootfs /out/rootfs.sqfs -comp zstd -noappend -quiet'
  echo "ok rootfs.sqfs"
fi

# rootfs EROFS (preferido; cai para squashfs se o kernel não suportar)
if [ ! -f "$STATE/rootfs.erofs" ] || [ -n "${FORCE_ROOTFS:-}" ]; then
  docker run --rm --privileged -v "$ROOT/rootfs:/rootfs:ro" -v "$STATE:/out" alpine:3.22 sh -c \
    'apk add -q erofs-utils >/dev/null 2>&1; rm -f /out/rootfs.erofs; mkfs.erofs -z lz4hc --all-root /out/rootfs.erofs /rootfs >/dev/null 2>&1'
  echo "ok rootfs.erofs"
fi

# disco de dados ext4 (o guest monta com -t ext4)
mkdir -p "$STATE"
if [ ! -f "$STATE/data.img" ]; then
  docker run --rm --privileged -v "$STATE:/out" alpine:3.22 sh -c \
    'apk add -q e2fsprogs >/dev/null 2>&1; truncate -s 8G /out/data.img; mkfs.ext4 -q -F /out/data.img'
  echo "ok data.img"
fi

# deixa os artefatos no layout usado pelo CLI (~/.nanoc/assets + rootfs.sqfs)
mkdir -p "$STATE/assets"
cp -f "$ASSETS/Image" "$ASSETS/initramfs.cpio.gz" "$ASSETS/modloop-virt" "$STATE/assets/" 2>/dev/null || true
# o marcador diz ao daemon que este modloop traz o nanoc_notify (cache de 10 s em /Users)
[ -f "$ASSETS/.modloop-notify" ] && cp -f "$ASSETS/.modloop-notify" "$STATE/assets/"
cp -f "$STATE/rootfs.sqfs" "$STATE/rootfs.sqfs" 2>/dev/null || true

echo "bootstrap concluído"
