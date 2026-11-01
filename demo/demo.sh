#!/usr/bin/env bash
# Demonstração do nano-container. Rode num Mac com `nanoc` instalado.
set -euo pipefail
export DOCKER_HOST="unix://$HOME/.nanoc/docker.sock"

say() { printf '\n\033[1;36m# %s\033[0m\n' "$*"; }

say "versão"
nanoc --version

say "saúde do ambiente"
nanoc doctor

say "sobe a VM (1 GB, sem GUI)"
nanoc up

say "roda um container"
docker run --rm alpine:3.22 uname -m

say "shell dentro do container (via agente, sem runc exec)"
docker run -d --name demo alpine:3.22 sleep 120 >/dev/null
nanoc exec demo -- sh -c 'head -2 /etc/os-release'
docker rm -f demo >/dev/null

say "máquina Linux avulsa"
nanoc machine create demo1
nanoc machine start demo1
nanoc machine ssh demo1 -- 'hostname; uname -r'
nanoc machine rm demo1

say "kubernetes opcional (desligado por padrão)"
nanoc k8s enable
KUBECONFIG="$HOME/.nanoc/kubeconfig" kubectl get nodes
nanoc k8s disable

say "pronto"
