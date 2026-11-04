#!/bin/sh
# Põe nanoc_notify.ko no modloop (modules/<versão>/extra), troca o virtiofs.ko pelo
# do virtiofs-hvc.patch quando build.sh o gerou, e refaz o modules.dep, para o
# guest carregar com modprobe. Mantém a compressão do Alpine (xz, filtro arm).
# uso: DOCKER_HOST=... ./pack-modloop.sh <modloop entrada> <modloop saída>
set -e
DIR=$(cd "$(dirname "$0")" && pwd)
[ -f "$DIR/nanoc_notify.ko" ] || { echo "falta $DIR/nanoc_notify.ko (rode build.sh)"; exit 1; }
# arquivos vão por stdin/stdout: o docker do guest só monta caminhos de /Users
T=$(mktemp -d)
trap 'rm -f "$T/in" "$T/ko" "$T/vfs" "$T/out"; rmdir "$T"' EXIT
cp "$1" "$T/in" && cp "$DIR/nanoc_notify.ko" "$T/ko"
: > "$T/vfs"
[ -f "$DIR/virtiofs.ko" ] && cp "$DIR/virtiofs.ko" "$T/vfs"
tar -C "$T" -cf - in ko vfs | docker run --rm -i ${PLATFORM:+--platform "$PLATFORM"} alpine:3.23 sh -ec "
exec 3>&1 1>&2
cd /tmp && tar -xf - && cp ko ko.ko
apk add -q squashfs-tools kmod >/dev/null
unsquashfs -q -no-progress -d sq in
U=\$(ls sq/modules | grep -v firmware | head -1)
V=\$(modinfo -F vermagic /tmp/ko.ko | cut -d' ' -f1)
[ \"\$V\" = \"\$U\" ] || { echo \"vermagic \$V não bate com o modloop (\$U)\"; exit 1; }
mkdir -p sq/modules/\$U/extra && cp ko sq/modules/\$U/extra/nanoc_notify.ko
if [ -s vfs ]; then
  cp vfs vfs.ko
  [ \"\$(modinfo -F vermagic /tmp/vfs.ko | cut -d' ' -f1)\" = \"\$U\" ] || { echo 'virtiofs.ko de outro kernel'; exit 1; }
  for old in sq/modules/\$U/kernel/fs/fuse/virtiofs.ko*; do rm -f \"\$old\"; done
  cp vfs sq/modules/\$U/kernel/fs/fuse/virtiofs.ko
fi
mkdir -p b/lib && ln -s /tmp/sq/modules b/lib/modules && depmod -b /tmp/b \$U
grep -q extra/nanoc_notify.ko sq/modules/\$U/modules.dep
mksquashfs sq out -comp xz -Xbcj arm,armthumb -b 131072 -all-root -noappend -quiet
cat out >&3
" > "$T/out"
mv "$T/out" "$2"
echo "ok $2 com nanoc_notify$([ -f "$DIR/virtiofs.ko" ] && echo " e virtiofs com hypercall")"
