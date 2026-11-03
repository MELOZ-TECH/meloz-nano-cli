#!/bin/sh
# testa o backend libkrun pelo CLI normal (up/status/ports/down) com um data.img de teste
K=$(dirname "$0")
N=$K/nanoc-krun
export NANOC_BACKEND=krun NANOC_DATA=$K/state/data.img NANOC_KRUN_INITRD=$K/initramfs-krun.cpio.gz
export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock
t0=$(date +%s)
$N up || { echo "UP FALHOU"; tail -30 $HOME/.nanoc/daemon.log; exit 1; }
echo "up em $(( $(date +%s) - t0 )) s"
$N status
docker rm -f web >/dev/null 2>&1
docker run -d --name web -p 8080:80 nginx:alpine >/dev/null && sleep 5
$N ports
curl -s -o /dev/null -w 'port forward http %{http_code}\n' http://127.0.0.1:8080/
mem() { echo "memória da VM ($1): $(top -l 1 -pid $(cat $HOME/.nanoc/nanoc.sock.pid) -stats mem | tail -1)"; }
mem "depois do up"
echo "exec sem stdin: $(docker exec web echo ok)"
echo "/Users no container: $(docker run --rm -v /Users:/Users alpine:latest ls /Users | tr '\n' ' ')"
echo "exec com stdin por pipe (limite conhecido, 10 s): $(echo hello | timeout 10 docker exec -i web cat; echo " rc=$?")"
for i in 1 2 3; do docker exec web wget -q -O /dev/null http://127.0.0.1/ && echo -n "hc$i "; done; echo
docker run --rm alpine:latest sh -c 'wget -qO /dev/null https://speed.cloudflare.com/__down?bytes=50000000 && echo download ok' &
s=$(date +%s); wait; echo "50 MB em $(( $(date +%s) - s )) s"
mem "logo depois do download"
sleep 60
mem "60 s depois (cache limpo a cada 45 s)"
t0=$(date +%s)
$N down
echo "down em $(( $(date +%s) - t0 )) s"
pgrep -f 'nanoc-krun daemon' >/dev/null && echo "DAEMON AINDA VIVO" || echo "daemon saiu"
