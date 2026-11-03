#!/bin/sh
# testa o half-close (stdin por pipe) no backend libkrun corrigido
K=$(dirname "$0")
N=$K/nanoc-krun
export NANOC_BACKEND=krun NANOC_DATA=$K/state/data.img NANOC_KRUN_INITRD=$K/initramfs-krun.cpio.gz
export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock
$N up || { echo "UP FALHOU"; tail -30 $HOME/.nanoc/daemon.log; exit 1; }
$N status | head -3
docker rm -f web >/dev/null 2>&1
docker run -d --name web nginx:alpine >/dev/null && sleep 3
echo "exec sem stdin: $(docker exec web echo ok)"
echo "stdin curto: $(echo hello | timeout 15 docker exec -i web cat; echo " rc=$?")"
echo "stdin vazio: [$(timeout 15 docker exec -i web cat </dev/null; echo "] rc=$?")"
head -c 8000000 /dev/urandom > $K/big.bin
a=$(shasum -a 256 < $K/big.bin | cut -c1-16)
b=$(timeout 60 docker exec -i web sha256sum < $K/big.bin | cut -c1-16)
echo "stdin 8 MB: mac=$a guest=$b $( [ "$a" = "$b" ] && echo IGUAL || echo DIFERENTE)"
c=$(timeout 60 docker exec -i web sh -c 'cat >/tmp/x; cat /tmp/x' < $K/big.bin | shasum -a 256 | cut -c1-16)
echo "ida e volta 8 MB: $c $( [ "$a" = "$c" ] && echo IGUAL || echo DIFERENTE)"
echo "docker run -i: $(printf 'a\nb\nc\n' | timeout 30 docker run -i --rm alpine:latest wc -l; echo " rc=$?")"
for i in 1 2 3 4 5; do docker exec web true && echo -n "exec$i "; done; echo
echo "tar por stdin: $(tar -C $K -cf - hc-test.sh | timeout 30 docker exec -i web tar -C /tmp -xf - && docker exec web ls /tmp/hc-test.sh)"
pid=$(cat $HOME/.nanoc/nanoc.sock.pid)
echo "daemon vivo depois dos testes: $(kill -0 $pid 2>/dev/null && echo sim || echo NÃO)"
$N down
pgrep -f 'nanoc-krun' >/dev/null && echo "DAEMON AINDA VIVO" || echo "daemon saiu"
