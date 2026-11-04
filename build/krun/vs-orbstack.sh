#!/bin/sh
# compara nanoc (libkrun) e OrbStack no mesmo Mac, uma VM de cada vez: boot até o
# Docker responder, CPU e memória com a VM parada, /Users (fsbench.sh) e memória
# depois da carga. uso: vs-orbstack.sh orb|nanoc
# O nanoc roda com outro HOME (estado e socket em $K/bh/.nanoc): quem usa o
# ~/.nanoc/docker.sock (o dbolt, pelo process-compose) subiria os containers dele
# na VM medida.
K=/Users/romeumelur/.nano/krunmtu
now() { python3 -c 'import time;print(time.time())'; }
# CPU média (%) e memória (MB, coluna MEM do top) da soma dos processos, em 30 s
usage() {
  top -l 31 -s 1 -stats pid,cpu,mem | python3 -I -c '
import sys, re
pids = set(sys.argv[1].split())
samples, cur, mem = [], 0.0, 0.0
def mb(v):
    m = re.match(r"([\d.]+)([BKMG])", v); n = float(m.group(1))
    return n * {"B": 1/2**20, "K": 1/1024, "M": 1, "G": 1024}[m.group(2)]
for line in sys.stdin:
    if line.startswith("Processes:"):
        samples.append((cur, mem)); cur, mem = 0.0, 0.0
        continue
    f = line.split()
    if len(f) == 3 and f[0] in pids:
        cur += float(f[1]); mem += mb(f[2].rstrip("+-"))
samples.append((cur, mem))
s = samples[2:]  # o primeiro quadro do top não tem CPU
print("CPU média %.1f%%, memória %.0f MB" % (sum(c for c, _ in s) / len(s), s[-1][1]))
' "$1"
}
case $1 in
orb)
  SOCK=$HOME/.orbstack/run/docker.sock
  start() { orbctl start >/dev/null 2>&1; }
  stop() { orbctl stop >/dev/null 2>&1; }
  pids() { pgrep -f 'OrbStack' | tr '\n' ' '; } ;;
nanoc)
  NH=$K/bh; SOCK=$NH/.nanoc/docker.sock
  start() { cd $K && HOME=$NH NANOC_MODLOOP=$K/modloop/modloop-virt NANOC_DATA_REL=bench-data.img NANOC_SHARE_DIR=$K \
    NANOC_BACKEND=krun NANOC_KRUN_INITRD=${NANOC_KRUN_INITRD:-$K/bin/initramfs-krun.cpio.gz} $K/bin/nanoc-krun up >/dev/null; }
  stop() { HOME=$NH NANOC_BACKEND=krun $K/bin/nanoc-krun down >/dev/null; }
  pids() { pgrep -f 'nanoc-krun' | tr '\n' ' '; } ;;
esac
export DOCKER_HOST=unix://$SOCK
ping() { curl -s --max-time 1 --unix-socket $SOCK http://d/_ping 2>/dev/null | grep -q OK; }
# aquecimento: cria o disco na primeira vez e baixa a imagem
start; until ping; do sleep 0.2; done; docker pull -q alpine:latest >/dev/null; stop; sleep 3
for i in 1 2 3; do
  t0=$(now); start; until ping; do sleep 0.05; done
  echo "boot $i até o Docker responder: $(python3 -c "print(round($(now)-$t0,1))") s"
  [ $i -lt 3 ] && { stop; sleep 3; }
done
sleep 60; echo "parada 60 s depois do boot: $(usage "$(pids)")"
t=""; for i in 1 2 3 4 5; do t0=$(now); docker run --rm alpine:latest true >/dev/null; t="$t $(python3 -c "print(round($(now)-$t0,2))")"; done
echo "docker run --rm alpine true (s):$t"
echo "containers na VM medida: $(docker ps -q | wc -l | tr -d ' ')"
$HOME/.nano/fsbench/fsbench.sh "$1"
echo "logo depois da carga: $(usage "$(pids)")"
sleep 60; echo "60 s depois da carga: $(usage "$(pids)")"
stop
