#!/usr/bin/env bash
# Compila um kernel arm64 (Alpine config) com EROFS e virtio-vsock habilitados.
# Uso: ./scripts/build-kernel.sh [versão]   (padrão: 6.12.114)
# Requer Docker com suporte a linux/arm64 e ~3 GB livres.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VER="${1:-6.12.114}"
OUT="$ROOT/.cache/kernel"
mkdir -p "$OUT"

docker run --rm --platform linux/arm64 -v "$OUT:/out" alpine:3.22 sh -c '
  apk add -q linux-virt >/dev/null 2>&1
  cp /boot/config-*-virt /out/config
' 

cat > "$OUT/build.sh" <<'INNER'
set -eux
cd /build
curl -fsSL -o linux.tar.xz "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${VER}.tar.xz"
tar xf linux.tar.xz
cd "linux-${VER}"
cp /out/config .config
sed -i "s/^CONFIG_GCC_PLUGINS=y/# CONFIG_GCC_PLUGINS is not set/" .config
sed -i 's|^CONFIG_MODULE_SIG_KEY=.*|CONFIG_MODULE_SIG_KEY="certs/signing_key.pem"|' .config
sed -i "s/^CONFIG_MODULE_SIG_FORCE=y/# CONFIG_MODULE_SIG_FORCE is not set/" .config
bash scripts/config --module EROFS_FS --module VIRTIO_VSOCKETS --module VSOCKETS \
  --module VIRTIO_VSOCKETS_COMMON --module VSOCKETS_DIAG
make olddefconfig
make -j"$(nproc)" Image modules
cp arch/arm64/boot/Image /out/Image
rm -rf /out/mods && make modules_install INSTALL_MOD_PATH=/out/mods
echo BUILD_OK
INNER

docker run --rm --platform linux/arm64 -m 6g -v "$OUT:/out" -v "$OUT:/build" \
  -e VER="$VER" alpine:3.22 sh -c '
  apk add -q bash build-base openssl gmp-dev mpfr-dev bison flex bc perl linux-headers openssl-dev elfutils-dev xz curl tar cpio kmod >/dev/null 2>&1
  sh /build/build.sh
'
echo "kernel em $OUT/Image"
