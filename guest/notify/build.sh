#!/bin/sh
# Compila nanoc_notify.ko para o kernel do modloop (Alpine linux-virt) num container
# alpine arm64. O módulo só carrega se a struct module e o vermagic baterem com o
# kernel. Caminho rápido: linux-virt-dev da mesma versão, se o repositório ainda o
# tiver. Senão: fonte exato do kernel.org, config do aports no commit da versão,
# pahole instalado (liga DEBUG_INFO_BTF_MODULES, que muda a struct module) e
# LOCALVERSION passado ao make, como no APKBUILD.
# uso: DOCKER_HOST=... ./build.sh [modloop-virt]   (padrão ~/.nanoc/assets/modloop-virt)
set -e
DIR=$(cd "$(dirname "$0")" && pwd)
MODLOOP=$(cd "$(dirname "${1:-$HOME/.nanoc/assets/modloop-virt}")" && pwd)/$(basename "${1:-modloop-virt}")
# arquivos vão por stdin/stdout: o docker do guest só monta caminhos de /Users
UNAME=$(docker run --rm -i ${PLATFORM:+--platform "$PLATFORM"} alpine:3.23 sh -c \
  'cat > /modloop && apk add -q squashfs-tools >/dev/null && unsquashfs -l /modloop | sed -n "s|^squashfs-root/modules/\([^/]*\)$|\1|p" | head -1' < "$MODLOOP")
KVER=${UNAME%%-*}          # 6.18.42
LOCALV=-${UNAME#*-}        # -0-virt
REL=r$(echo "$UNAME" | cut -d- -f2)
# commit do aports em que main/linux-lts passou a cada versão (só para o caminho lento)
case $KVER in
  6.18.42) APORTS=c6607eb3f1eb ;;
  *) APORTS= ;;
esac
echo "kernel do modloop: $UNAME"
docker volume create nanoc-kbuild >/dev/null
tar -C "$DIR" -cf - nanoc_notify.c Kbuild | docker run --rm -i ${PLATFORM:+--platform "$PLATFORM"} -v nanoc-kbuild:/k alpine:3.23 sh -ec "
mkdir /mod && tar -xf - -C /mod
exec 3>&1 1>&2
apk add -q build-base elfutils-dev >/dev/null
if apk add -q linux-virt-dev=$KVER-$REL >/dev/null 2>&1; then
  make -s -C /usr/src/linux-headers-$UNAME M=/mod modules
  cat /mod/nanoc_notify.ko >&3
  exit 0
fi
[ -n '$APORTS' ] || { echo 'linux-virt-dev $KVER fora do repositório e sem commit do aports para $KVER em build.sh'; exit 1; }
apk add -q bc flex bison perl openssl-dev linux-headers gmp-dev mpc1-dev mpfr-dev pahole python3 curl xz >/dev/null
cd /k
[ -d linux-$KVER ] || curl -fsSL https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-$KVER.tar.xz | tar xJ
cd linux-$KVER
curl -fsSL https://gitlab.alpinelinux.org/alpine/aports/-/raw/$APORTS/main/linux-lts/virt.aarch64.config -o .config
make -s olddefconfig
make -s LOCALVERSION=$LOCALV -j\$(nproc) modules_prepare
make -s LOCALVERSION=$LOCALV M=/mod KBUILD_MODPOST_WARN=1 modules 2>&1 | grep -v 'undefined!' || true
cat /mod/nanoc_notify.ko >&3
" > "$DIR/nanoc_notify.ko.tmp"
mv "$DIR/nanoc_notify.ko.tmp" "$DIR/nanoc_notify.ko"
V=$(LC_ALL=C strings "$DIR/nanoc_notify.ko" | sed -n 's/^vermagic=\([^ ]*\).*/\1/p')
[ "$V" = "$UNAME" ] || { echo "vermagic $V não bate com $UNAME"; exit 1; }
echo "ok nanoc_notify.ko ($V)"
