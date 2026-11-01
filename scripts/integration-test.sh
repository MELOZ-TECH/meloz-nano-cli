#!/usr/bin/env bash
# Teste de integração real: sobe a VM, roda um container, derruba.
# Requer macOS arm64 com Virtualization.framework (não roda em CI aninhado).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BIN=./nanoc
SOCK="$HOME/.nanoc/docker.sock"
export DOCKER_HOST="unix://$SOCK"

echo "== up"
"$BIN" up

cleanup() { "$BIN" down || true; }
trap cleanup EXIT

echo "== docker version"
docker version --format 'client={{.Client.Version}} server={{.Server.Version}}'

echo "== run"
out=$(docker run --rm alpine:3.22 echo nanoc-ok)
[ "$out" = "nanoc-ok" ] || { echo "falhou: $out"; exit 1; }

echo "== driver"
driver=$(docker info --format '{{.Driver}}')
[ "$driver" = "overlay2" ] || { echo "driver inesperado: $driver"; exit 1; }

echo "OK"
