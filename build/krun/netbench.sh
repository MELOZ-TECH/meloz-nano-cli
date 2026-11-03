#!/bin/sh
# mede a rede Mac <-> container de um docker, 3 rodadas de 5 s por direção
# uso: netbench.sh <rótulo> <ip do Mac visto do guest>
L=$1; HOSTIP=$2
docker rm -f nb >/dev/null 2>&1
docker run -d --name nb ${NBNET:+--network $NBNET} -p 5201:5201 alpine:latest sh -c 'apk add -q iperf3 >/dev/null && iperf3 -s' >/dev/null
for i in $(seq 1 60); do docker exec nb sh -c 'command -v iperf3' >/dev/null 2>&1 && break; sleep 1; done; sleep 1
echo "== $L"
echo "VM eth0: $(docker run --rm --network host alpine sh -c 'apk add -q ethtool >/dev/null; echo mtu $(cat /sys/class/net/eth0/mtu); ethtool -k eth0 | grep -E "^(tx-checksumming|tcp-segmentation-offload|generic-receive-offload|scatter-gather):"' 2>/dev/null | tr '\n' ' ')"
bps() { python3 -c 'import json,sys;d=json.load(sys.stdin);print(round(d["end"]["sum_received"]["bits_per_second"]/1e6))'; }
r() { o=""; for i in 1 2 3; do o="$o $("$@" | bps)"; done; echo "$o"; }
echo "Mac -> container (port forward):    $(r iperf3 -c 127.0.0.1 -t 5 -J) Mbit/s"
echo "container -> Mac (port forward -R): $(r iperf3 -c 127.0.0.1 -t 5 -R -J) Mbit/s"
iperf3 -s -D -p 5202 -I ${TMPDIR:-/tmp}/nanoc-iperf.pid >/dev/null 2>&1; sleep 1
echo "container -> Mac ($HOSTIP):     $(r docker exec nb iperf3 -c $HOSTIP -p 5202 -t 5 -J) Mbit/s"
echo "Mac -> container ($HOSTIP, -R): $(r docker exec nb iperf3 -c $HOSTIP -p 5202 -t 5 -R -J) Mbit/s"
kill $(cat ${TMPDIR:-/tmp}/nanoc-iperf.pid) 2>/dev/null
docker rm -f nb >/dev/null
