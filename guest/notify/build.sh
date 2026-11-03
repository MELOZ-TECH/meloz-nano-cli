#!/bin/sh
# Compila nanoc_notify.ko para o kernel do guest (Alpine linux-virt 6.18.42-0-virt) num
# container alpine do próprio nanoc. O módulo só carrega se a struct module e o
# vermagic baterem com o kernel: fonte exato do kernel.org, config do aports no
# commit da versão, pahole instalado (liga DEBUG_INFO_BTF_MODULES, que muda a struct
# module) e LOCALVERSION passado ao make, como no APKBUILD.
# uso: DOCKER_HOST=unix://$HOME/.nanoc/docker.sock ./build.sh
set -e
KVER=6.18.42
APORTS=c6607eb3f1eb   # main/linux-lts: upgrade to 6.18.42
DIR=$(cd "$(dirname "$0")" && pwd)
docker volume create nanoc-kbuild >/dev/null
docker run --rm -v nanoc-kbuild:/k -v "$DIR":/mod alpine:3.23 sh -ec "
apk add -q build-base bc flex bison perl openssl-dev elfutils-dev linux-headers \
  gmp-dev mpc1-dev mpfr-dev pahole curl xz >/dev/null
cd /k
[ -d linux-$KVER ] || curl -fsSL https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-$KVER.tar.xz | tar xJ
cd linux-$KVER
curl -fsSL https://gitlab.alpinelinux.org/alpine/aports/-/raw/$APORTS/main/linux-lts/virt.aarch64.config -o .config
make -s olddefconfig
make -s LOCALVERSION=-0-virt -j4 modules_prepare || true
make -s LOCALVERSION=-0-virt M=/mod KBUILD_MODPOST_WARN=1 modules 2>&1 | grep -v 'undefined!' || true
"
strings "$DIR/nanoc_notify.ko" | grep '^vermagic='
