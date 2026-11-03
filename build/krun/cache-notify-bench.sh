#!/bin/sh
# mede /Users com cache curto e longo, com e sem os avisos do nanoc_notify, numa VM
# libkrun de teste (disco de teste). Para cada configuração:
#  - velocidade: um container lê e faz stat de 10 mil arquivos de novo, 1 s depois
#    da passada anterior (como um watcher ou um build que volta à árvore);
#  - correção: o Mac muda arquivos e o container confere em quanto tempo enxerga.
# uso: cache-notify-bench.sh <ms>[:off] ...   (ex.: 100 5000 60000 60000:off; auto = padrão do daemon)
HERE=$(cd "$(dirname "$0")" && pwd)
export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock
K=/Users/romeumelur/.nano/krunmtu
B=$HOME/.nano/fsbench/cachebench
if [ ! -d $B/tree ]; then
  python3 -I -c "
import os,sys
for d in range(100):
    p=os.path.join(sys.argv[1],'tree','d%03d'%d); os.makedirs(p)
    for f in range(100):
        open(os.path.join(p,'f%03d.txt'%f),'w').write('x'*512)
" $B
fi
mkdir -p $B/live

# poll dentro do container: ms até a condição valer, ou FALHOU depois de 3 s
seen() { docker exec cb sh -c "S=\$(date +%s%N); while ! { $1; } 2>/dev/null; do [ \$(( (\$(date +%s%N)-S)/1000000 )) -gt 3000 ] && { echo FALHOU; exit; }; done; echo \$(( (\$(date +%s%N)-S)/1000000 )) ms"; }

for cfg in "$@"; do
  ms=${cfg%%:*}; notify=1; [ "$cfg" != "$ms" ] && notify=0
  for f in a.txt b.txt c.txt d.txt e.txt novo.txt; do [ -e $B/live/$f ] && rm $B/live/$f; done
  [ -d $B/live/sub ] && rm $B/live/sub/x/f && rmdir $B/live/sub/x $B/live/sub
  echo abc > $B/live/a.txt; echo old > $B/live/b.txt; echo abcd > $B/live/c.txt
  echo apagar > $B/live/d.txt; echo modo > $B/live/e.txt; chmod 644 $B/live/e.txt
  n0=$(wc -l < $HOME/.nanoc/events.log)
  cd $K && NANOC_KRUN_NOTIFY=$notify NANOC_MODLOOP=$K/modloop/modloop-virt NANOC_KRUN_USERS_CACHE_MS=$ms \
    NANOC_DATA_REL=test-data.img NANOC_SHARE_DIR=$K NANOC_BACKEND=krun \
    NANOC_KRUN_INITRD=$K/bin/initramfs-krun.cpio.gz $K/bin/nanoc-krun up >/dev/null || { echo UP FALHOU; exit 1; }
  docker rm -f cb >/dev/null 2>&1
  docker run -d --name cb -v $B/tree:/w/tree -v $B/live:/live alpine:latest sleep 3600 >/dev/null
  # o date do busybox não tem %N
  docker exec cb apk add -q coreutils >/dev/null
  if [ $notify = 1 ]; then
    until tail -n +$((n0+1)) $HOME/.nanoc/events.log | grep -q "notify_watch.*cachebench/live"; do sleep 1; done
  fi
  echo "== cache $ms ms, avisos $([ $notify = 1 ] && echo ligados || echo desligados)"
  docker exec cb sh -c '
    t() { s=$(date +%s%N); eval "$1" >/dev/null 2>&1; echo $(( ($(date +%s%N)-s)/1000000 )); }
    st="find /w/tree -type f -exec stat -c %s {} +"; rd="cat /w/tree/*/*"
    t "$st" >/dev/null; t "$rd" >/dev/null
    a=""; b=""
    for i in 1 2 3 4 5; do sleep 1; a="$a $(t "$st")"; sleep 1; b="$b $(t "$rd")"; done
    echo "stat de 10 mil, de novo 1 s depois (ms):$a"
    echo "ler 10 mil, de novo 1 s depois (ms):   $b"'
  # o container já viu tudo (e a falta de novo.txt e sub), então o cache está quente
  docker exec cb sh -c 'cat /live/*.txt; stat -c %a /live/e.txt; test -e /live/novo.txt; test -e /live/sub' >/dev/null 2>&1
  sleep 1
  echo "abcdefghijklmnop" > $B/live/a.txt;                   echo "arquivo cresceu:        $(seen '[ "$(cat /live/a.txt)" = abcdefghijklmnop ]')"
  echo wxyz > $B/live/c.txt;                                 echo "mesmo tamanho, outro conteúdo: $(seen '[ "$(cat /live/c.txt)" = wxyz ]')"
  echo novo > $B/live/novo.txt;                              echo "arquivo novo:           $(seen '[ "$(cat /live/novo.txt)" = novo ]')"
  rm $B/live/d.txt;                                          echo "arquivo apagado:        $(seen '! test -e /live/d.txt')"
  mv $B/live/b.txt $B/live/b2.txt;                           echo "renomeado (nome velho): $(seen '! test -e /live/b.txt')"
  echo                                                            "renomeado (nome novo):  $(seen '[ "$(cat /live/b2.txt)" = old ]')"
  printf 'salvo' > $B/live/.a.tmp && mv $B/live/.a.tmp $B/live/a.txt; echo "salvo como editor:      $(seen '[ "$(cat /live/a.txt)" = salvo ]')"
  chmod 600 $B/live/e.txt;                                   echo "chmod:                  $(seen '[ "$(stat -c %a /live/e.txt)" = 600 ]')"
  mkdir -p $B/live/sub/x && echo f > $B/live/sub/x/f;        echo "pasta nova com arquivo: $(seen '[ "$(cat /live/sub/x/f)" = f ]')"
  rm $B/live/b2.txt
  docker rm -f cb >/dev/null
  NANOC_BACKEND=krun $K/bin/nanoc-krun down >/dev/null
done
