#!/bin/sh
# Compila o libkrun que o backend libkrun do nanoc usa: o main upstream num commit
# fixo mais o patch do nanoc (PRs ainda abertos no libkrun; ver README.md).
# É pesado (vários minutos e GBs de RAM): no CI, ou num Mac de 8 GB com a VM do
# nanoc parada e CARGO_BUILD_JOBS=2.
# uso: build/krun/build-libkrun.sh <pasta do clone>   (clona se não existir)
# Ao fim, <pasta>/target/release/libkrun.dylib e <pasta>/include/libkrun.h.
set -e
COMMIT=302d447d
HERE=$(cd "$(dirname "$0")" && pwd)
DIR=${1:?pasta do clone do libkrun}
# init + fetch no lugar de clone: o cache do CI recria <pasta>/target antes daqui
if [ ! -e "$DIR/.git" ]; then
  mkdir -p "$DIR"
  git -C "$DIR" init -q
  git -C "$DIR" remote add origin https://github.com/libkrun/libkrun.git
fi
cd "$DIR"
git fetch -q origin
git checkout -q --detach "$COMMIT"
git reset -q --hard
git apply "$HERE/libkrun-nanoc-$COMMIT.patch"
cargo build --release -p libkrun --no-default-features --features blk,net,ffi
