#!/bin/sh
# Remonta o libkrun do nanoc enquanto os PRs não entram no upstream: o main do
# libkrun (ou BASE=<commit>) mais os branches dos PRs ainda abertos e os só do
# fork, e regenera libkrun-nanoc-<commit>.patch, que build-libkrun.sh e o CI
# usam. Não compila.
#
# uso: build/krun/update-libkrun.sh [clone do libkrun]   (padrão ~/.nano/src/libkrun)
#      BASE=302d447d build/krun/update-libkrun.sh          (fixa a base)
#
# O clone precisa dos remotes origin (libkrun/libkrun) e fork (romeumellobr/libkrun).
# PR que já entrou no upstream é pulado e avisado: tire-o de PRS.
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
DIR=${1:-$HOME/.nano/src/libkrun}
# número do PR no libkrun/libkrun : branch no fork
PRS="973:vsock-unix-half-close 974:virtiofs-cache-timeouts 975:hvf-gic-wake-wfi 978:vsock-unix-nonblocking-send 979:net-unixgram-vnet-hdr"
# branches só do fork, sem PR no upstream (o protocolo é do nanoc)
FORK="virtiofs-vendor-hvc balloon-reclaim-64k virtiofs-xattr-list"

TESTS=tests/test_cases/src/lib.rs

# Junta os dois lados de cada conflito, repondo o #[cfg(...)] que o lado de baixo
# perde por ele ficar fora do trecho em conflito.
keep_both() {
  python3 - "$1" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p).read()
def both(m):
    a, b = m.group(1), m.group(2)
    cfg = next((l for l in reversed(s[:m.start()].splitlines(True)) if l.strip().startswith("#[cfg(")), None)
    if cfg is None:
        raise SystemExit(1)
    return a + cfg + b
s2 = re.sub(r"<<<<<<< [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n", both, s, flags=re.S)
if "<<<<<<<" in s2:
    raise SystemExit(1)
open(p, "w").write(s2)
PY
}

cd "$DIR"
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "o clone em $DIR tem mudanças sem commit"; exit 1; }
git fetch -q origin
git fetch -q fork
BASE=$(git rev-parse --short=8 "${BASE:-origin/main}")
echo "base: $BASE ($(git log -1 --format=%s "$BASE"))"
git checkout -q -B nanoc-build "$BASE"

for pr in $PRS; do
  n=${pr%%:*}
  b=${pr#*:}
  state=$(gh pr view "$n" --repo libkrun/libkrun --json state -q .state 2>/dev/null || echo "?")
  if [ "$state" = MERGED ]; then
    echo "#$n já entrou no upstream: pulado (tire-o de PRS)"
    continue
  fi
  if git merge-base --is-ancestor "fork/$b" HEAD; then
    echo "#$n ($state): já contido na base"
    continue
  fi
  if ! git -c user.name=nanoc -c user.email=nanoc@localhost merge -q --no-edit "fork/$b" >/dev/null 2>&1; then
    # os PRs acrescentam testes no mesmo ponto da lista; esse conflito se resolve
    # mantendo os dois lados, cada um com o seu #[cfg]. Qualquer outro, não.
    if [ "$(git diff --name-only --diff-filter=U)" = "$TESTS" ] && keep_both "$TESTS"; then
      git add "$TESTS"
      git -c user.name=nanoc -c user.email=nanoc@localhost commit -q --no-edit
      echo "#$n ($state): aplicado (lista de testes juntada)"
      continue
    fi
    echo "#$n ($state): CONFLITO ao juntar fork/$b; resolva no branch do PR e rode de novo"
    git merge --abort
    exit 1
  fi
  echo "#$n ($state): aplicado"
done

for b in $FORK; do
  if git merge-base --is-ancestor "fork/$b" HEAD; then
    echo "$b (fork): já contido"
    continue
  fi
  git -c user.name=nanoc -c user.email=nanoc@localhost merge -q --no-edit "fork/$b" >/dev/null 2>&1 ||
    { echo "$b (fork): CONFLITO; resolva no branch e rode de novo"; git merge --abort; exit 1; }
  echo "$b (fork): aplicado"
done

OLD=$(sed -n 's/^COMMIT=//p' "$HERE/build-libkrun.sh")
git diff "$BASE" nanoc-build > "$HERE/libkrun-nanoc-$BASE.patch"
if [ "$OLD" != "$BASE" ]; then
  [ -f "$HERE/libkrun-nanoc-$OLD.patch" ] && rm "$HERE/libkrun-nanoc-$OLD.patch"
  sed -i '' "s/$OLD/$BASE/g" "$HERE/build-libkrun.sh" "$HERE/README.md"
  echo "base trocada: $OLD -> $BASE (build-libkrun.sh e README.md)"
fi
echo "patch: $HERE/libkrun-nanoc-$BASE.patch ($(wc -l < "$HERE/libkrun-nanoc-$BASE.patch" | tr -d ' ') linhas)"
echo "o CI compila e testa ao subir o commit; para compilar aqui: build/krun/build-libkrun.sh $DIR"
