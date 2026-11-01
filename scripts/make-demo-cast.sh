#!/usr/bin/env bash
# Gera demo/nanoc-demo.cast executando comandos reais (asciinema v2).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/demo/nanoc-demo.cast"
TMP=$(mktemp)

run() { # comando exibido
  printf '\n\033[1;36m$ %s\033[0m\n' "$*" >> "$TMP"
  eval "$@" 2>&1 | sed 's/\r$//' >> "$TMP" || true
}

export DOCKER_HOST="unix://$HOME/.nanoc/docker.sock"
run "nanoc --version"
run "nanoc up"
run "docker run --rm alpine:3.22 uname -m"
run "nanoc machine create demo1 && nanoc machine start demo1"
run "nanoc machine ssh demo1 -- 'hostname; uname -r'"
run "nanoc machine rm demo1"

python3 - "$TMP" "$OUT" <<'PY'
import json, sys, time
src, out = sys.argv[1], sys.argv[2]
lines = open(src, encoding="utf-8").read().splitlines()
header = {"version": 2, "width": 92, "height": 26, "timestamp": int(time.time()),
          "env": {"SHELL": "/bin/zsh", "TERM": "xterm-256color"}}
with open(out, "w", encoding="utf-8") as f:
    f.write(json.dumps(header) + "\n")
    t = 0.0
    for line in lines:
        t += 0.35 if line.strip() else 0.05
        f.write(json.dumps([round(t, 3), "o", line + "\r\n"]) + "\n")
print(f"{out}: {len(lines)} linhas")
PY
rm -f "$TMP"
