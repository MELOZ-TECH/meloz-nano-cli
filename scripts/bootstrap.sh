#!/usr/bin/env bash
# Baixa e monta os artefatos do guest (kernel, initramfs, modloop, rootfs, data.img).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALPINE_VER="${ALPINE_VER:-3.22}"
ARCH="${ARCH:-aarch64}"
BASE="https://dl-cdn.alpinelinux.org/alpine/v${ALPINE_VER}/releases/${ARCH}/netboot"
ASSETS="$ROOT/assets"
CACHE="$ROOT/.cache"

mkdir -p "$ASSETS" "$CACHE"

fetch() { # url dest
  [ -f "$2" ] || curl -fsSL --retry 3 -o "$2" "$1"
  echo "ok $(basename "$2")"
}

fetch "$BASE/vmlinuz-virt" "$ASSETS/vmlinuz-virt"
fetch "$BASE/initramfs-virt" "$ASSETS/initramfs-virt"
fetch "$BASE/modloop-virt" "$ASSETS/modloop-virt"

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
  docker build -q -t nano-rootfs "$ROOT/guest" >/dev/null
  cid=$(docker create nano-rootfs)
  mkdir -p "$ROOT/rootfs"
  docker export "$cid" | tar -x -C "$ROOT/rootfs"
  docker rm "$cid" >/dev/null
  echo "ok rootfs"
fi

# binários do guest (estáticos, linux/arm64)
( cd "$ROOT/guestinit" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='-s -w' -o "$ROOT/rootfs/guestinit" . )
( cd "$ROOT/agent" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='-s -w' -o "$ROOT/rootfs/opt/nano-agent" . )
cp "$ROOT/guest/nano-guest.sh" "$ROOT/rootfs/opt/nano-guest.sh"
chmod +x "$ROOT/rootfs/opt/nano-guest.sh" "$ROOT/rootfs/opt/nano-agent" "$ROOT/rootfs/guestinit"

# initramfs com o init próprio
rm -rf "$CACHE/initrd" && mkdir -p "$CACHE/initrd"
gzip -dc "$ASSETS/initramfs-virt" | ( cd "$CACHE/initrd" && cpio -idm --quiet )
cp "$ROOT/rootfs/guestinit" "$CACHE/initrd/guestinit"
chmod +x "$CACHE/initrd/guestinit"
( cd "$CACHE/initrd" && find . | cpio -o -H newc --quiet | gzip -9 ) > "$ASSETS/initramfs.cpio.gz"
echo "ok initramfs"

# disco de dados btrfs
STATE="${NANO_STATE:-$HOME/.nano}"
mkdir -p "$STATE"
if [ ! -f "$STATE/data.img" ]; then
  docker run --rm --privileged -v "$STATE:/out" alpine:3.22 sh -c \
    'apk add -q btrfs-progs >/dev/null 2>&1; truncate -s 8G /out/data.img; mkfs.btrfs -q -f /out/data.img'
  echo "ok data.img"
fi

echo "bootstrap concluído"
