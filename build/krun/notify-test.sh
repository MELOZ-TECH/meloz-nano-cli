#!/bin/sh
# testa o módulo nanoc_notify numa VM libkrun de teste (disco de teste), com /Users em cache de 5 s
export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock
G="python3 -I $(dirname "$0")/guestrun.py"
D=$HOME/.nano/fsbench/watch; mkdir -p $D; rm -f $D/*
echo abc > $D/c.txt; echo velho > $D/apagar.txt
cd /Users/romeumelur/.nano/krunmtu && NANOC_KRUN_USERS_CACHE_MS=5000 NANOC_DATA_REL=test-data.img NANOC_SHARE_DIR=/Users/romeumelur/.nano/krunmtu NANOC_BACKEND=krun NANOC_KRUN_INITRD=/Users/romeumelur/.nano/krunmtu/bin/initramfs-krun.cpio.gz /Users/romeumelur/.nano/krunmtu/bin/nanoc-krun up >/dev/null || { echo UP FALHOU; exit 1; }
sleep 3
echo "--- insmod: $($G sh -c 'insmod /data/nanoc_notify.ko && ls -l /dev/nanoc_notify; dmesg | tail -4')"
docker rm -f iw >/dev/null 2>&1
docker run -d --name iw -v $D:/w alpine:latest sh -c 'apk add -q inotify-tools >/dev/null && touch /tmp/ok && inotifywait -m -r --format "%e %f" /w' >/dev/null
until docker exec iw test -f /tmp/ok 2>/dev/null; do sleep 1; done; sleep 2
docker exec iw cat /w/c.txt /w/apagar.txt >/dev/null
P=$D
inj() { $G sh -c "printf '%s %s\n' $1 $2 > /dev/nanoc_notify"; }
echo "abcdefghijklmnop-cresceu-no-mac" > $D/c.txt
echo "cresceu, sem aviso:   $(docker exec iw cat /w/c.txt)"
inj m $P/c.txt
echo "cresceu, com aviso:   $(docker exec iw cat /w/c.txt)"
echo novo > $D/novo.txt; inj c $P/novo.txt
rm $D/apagar.txt; inj d $P/apagar.txt
echo "apagado ainda visível? $(docker exec iw sh -c 'test -e /w/apagar.txt && echo sim || echo não')"
chmod 600 $D/c.txt; inj a $P/c.txt
sleep 1
echo "--- eventos no container:"; docker logs iw 2>&1 | grep -v Watches | grep -v "Setting up"
echo "--- kernel: $($G sh -c 'cat /proc/sys/kernel/tainted; dmesg | grep -iE "nanoc_notify|oops|bug|warn" | tail -5')"
docker rm -f iw >/dev/null
NANOC_BACKEND=krun /Users/romeumelur/.nano/krunmtu/bin/nanoc-krun down >/dev/null
