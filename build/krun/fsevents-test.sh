#!/bin/sh
# testa o caminho inteiro do hot reload numa VM libkrun de teste (disco de teste),
# com /Users em cache de 5 s: o Mac muda arquivos, o FSEvents do daemon avisa o
# /dev/nanoc_notify do guest e o inotifywait de um container recebe os eventos.
# Nenhum aviso é injetado à mão.
HERE=$(cd "$(dirname "$0")" && pwd)
export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock
K=/Users/romeumelur/.nano/krunmtu
D=$HOME/.nano/fsbench/watch; mkdir -p $D
for f in c.txt apagar.txt novo.txt proprio.txt; do [ -e $D/$f ] && rm $D/$f; done
echo abc > $D/c.txt; echo velho > $D/apagar.txt
cd $K && NANOC_KRUN_NOTIFY_KO=$K/nanoc_notify.ko NANOC_KRUN_USERS_CACHE_MS=5000 NANOC_DATA_REL=test-data.img \
  NANOC_SHARE_DIR=$K NANOC_BACKEND=krun NANOC_KRUN_INITRD=$K/bin/initramfs-krun.cpio.gz $K/bin/nanoc-krun up >/dev/null || { echo UP FALHOU; exit 1; }
docker rm -f iw >/dev/null 2>&1
docker run -d --name iw -v $D:/w alpine:latest sh -c 'apk add -q inotify-tools >/dev/null && touch /tmp/ok && inotifywait -m -r --format "%e %f" /w' >/dev/null
until docker exec iw test -f /tmp/ok 2>/dev/null; do sleep 1; done
until grep -q "notify_watch.*fsbench" $HOME/.nanoc/events.log 2>/dev/null; do sleep 1; done
grep -E 'notify_(ready|off|watch)' $HOME/.nanoc/events.log | tail -2
docker exec iw cat /w/c.txt /w/apagar.txt >/dev/null
echo "abcdefghijklmnop-cresceu-no-mac" > $D/c.txt
t0=$(python3 -c 'import time;print(time.time())')
until [ "$(docker exec iw cat /w/c.txt)" = "abcdefghijklmnop-cresceu-no-mac" ]; do
  [ $(python3 -c "import time;print(int(time.time()-$t0>4))") = 1 ] && { echo "cresceu: VELHO depois de 4 s"; break; }
done
echo "cresceu no Mac, conteúdo novo no container em $(python3 -c "import time;print(int((time.time()-$t0)*1000))") ms"
echo novo > $D/novo.txt
rm $D/apagar.txt
chmod 600 $D/c.txt
sleep 1
echo "apagado ainda visível? $(docker exec iw sh -c 'test -e /w/apagar.txt && echo sim || echo não')"
docker exec iw sh -c 'echo do-container > /w/proprio.txt'
sleep 1
echo "--- eventos no container:"; docker logs iw 2>&1 | grep -v Watches | grep -v "Setting up"
echo "--- kernel: $(python3 -I $HERE/guestrun.py sh -c 'cat /proc/sys/kernel/tainted; dmesg | grep -iE "nanoc_notify|oops|bug|warn" | tail -5')"
docker rm -f iw >/dev/null
NANOC_BACKEND=krun $K/bin/nanoc-krun down >/dev/null
