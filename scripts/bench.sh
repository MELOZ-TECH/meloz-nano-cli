#!/usr/bin/env bash
# Mede boot, RAM, primeiro container e tamanho do estado. Uso: ./scripts/bench.sh
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STATE="${NANOC_STATE:-$HOME/.nanoc}"
BIN="${NANOC_BIN:-$(command -v nanoc || echo "$ROOT/nanoc")}"
DOCKER="unix://$STATE/docker.sock"

now() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
elapsed() { python3 -c "print(f'{$2-$1:.2f}')"; }

echo "== nanoc bench =="
echo "binário: $("$BIN" --version 2>/dev/null || echo '?')"
echo "assets:  $(ls "$STATE/assets" 2>/dev/null | wc -l | tr -d ' ') arquivos"

# boot limpo
"$BIN" down >/dev/null 2>&1 || true
pkill -9 -f "nanoc daemon" 2>/dev/null || true
sleep 2
BOOTS=()
for i in 1 2 3; do
  "$BIN" down >/dev/null 2>&1 || true
  pkill -9 -f "nanoc daemon" 2>/dev/null || true
  sleep 1
  t0=$(now)
  "$BIN" up >/dev/null 2>&1
  t1=$(now)
  BOOTS+=("$(elapsed "$t0" "$t1")")
done
BOOT=$(python3 -c "v=sorted([${BOOTS[0]},${BOOTS[1]},${BOOTS[2]}]); print(f'{v[1]:.2f}')")

export DOCKER_HOST="$DOCKER"
# RAM de repouso (guest)
MEMTOTAL=$(docker run --rm alpine:3.22 sh -c "awk '/MemTotal/{print \$2}' /proc/meminfo" 2>/dev/null || echo 0)
MEMUSED=$(docker run --rm alpine:3.22 sh -c "awk '/MemAvailable/{print \$2}' /proc/meminfo" 2>/dev/null || echo 0)

# primeiro container (imagem já presente)
docker pull -q alpine:3.22 >/dev/null 2>&1 || true
t2=$(now)
docker run --rm alpine:3.22 sh -c 'echo ok' >/dev/null 2>&1
t3=$(now)
FIRST=$(elapsed "$t2" "$t3")

# tamanho do estado
STATE_KB=$(du -sk "$STATE" 2>/dev/null | awk '{print $1}')
size_mb() { python3 -c "import os,sys; print(int(os.path.getsize(os.path.realpath(sys.argv[1])))/1048576)" "$1" 2>/dev/null || echo 0; }
DATA_MB=$(size_mb "$STATE/data.img")
ROOTFS_MB=$(size_mb "$STATE/rootfs.erofs")
BIN_MB=$(size_mb "$BIN")

cat <<RESULT

| Métrica | Valor |
|---|---|
| Boot (\`nanoc up\` até pronto) | ${BOOT} s |
| RAM do guest (total) | $((MEMTOTAL/1024)) MB |
| RAM disponível em repouso | $((MEMUSED/1024)) MB |
| Primeiro \`docker run\` (imagem local) | ${FIRST} s |
| Estado em disco (alocado) | $((STATE_KB/1024)) MB |
| data.img (esparso) | ${DATA_MB%%.*} MB |
| rootfs.erofs | ${ROOTFS_MB%%.*} MB |
| Binário \`nanoc\` | ${BIN_MB:0:4} MB |
RESULT
