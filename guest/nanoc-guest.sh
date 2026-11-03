#!/bin/sh
# Boot do guest: rede, dados (ext4 em loop), dockerd e agente do docker.sock.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

mkdir -p /sys/fs/cgroup /run /tmp
mount -t cgroup2 none /sys/fs/cgroup 2>/dev/null
mount -t tmpfs tmpfs /run 2>/dev/null
mount -t tmpfs tmpfs /tmp 2>/dev/null

# memória (baseado no guest do OrbStack). O balão do VZ não devolve ao macOS
# página já tocada, então o guest deve tocar o mínimo e comprimir o que esfria em
# vez de pedir RAM nova ao host. Por isso swappiness 100 (o OrbStack usa 20, mas
# ele devolve memória ao host e nós não).
sysctl -q -w vm.overcommit_memory=1 vm.watermark_boost_factor=0 \
  vm.compaction_proactiveness=0 vm.page-cluster=0 vm.swappiness=100 2>/dev/null
# MGLRU: min_ttl_ms protege do despejo o que foi usado no último meio segundo
echo 500 > /sys/kernel/mm/lru_gen/min_ttl_ms 2>/dev/null
# THP de 2 MB só sob madvise (com "always" um toque de 4 KB custa 2 MB no Mac);
# folios de 16 KB continuam automáticos
THP=/sys/kernel/mm/transparent_hugepage
echo madvise > $THP/enabled 2>/dev/null
echo always > $THP/hugepages-16kB/enabled 2>/dev/null
# zram como swap: até o tamanho da RAM do guest, comprimida com lz4
if ! grep -q nanoc.nozram /proc/cmdline && modprobe zram num_devices=1 2>/dev/null; then
  ZMEM=$(awk '/^MemTotal/ { print $2 }' /proc/meminfo)
  echo lz4 > /sys/block/zram0/comp_algorithm 2>/dev/null
  echo "${ZMEM}K" > /sys/block/zram0/disksize && mkswap /dev/zram0 >/dev/null 2>&1 &&
    swapon -p 32767 /dev/zram0 2>/dev/null
fi

IP=$(sed -n 's/.*nanoc\.ip=\([0-9.]*\).*/\1/p' /proc/cmdline)
[ -n "$IP" ] || IP=192.168.64.2
ip link set lo up 2>/dev/null
ip link set eth0 up 2>/dev/null
# nanoc.mtu (libkrun): quadros grandes até a pilha de rede do host. A placa não
# tem TSO, então cada pacote do guest vai inteiro num quadro só.
MTU=$(sed -n 's/.*nanoc\.mtu=\([0-9]*\).*/\1/p' /proc/cmdline)
[ -n "$MTU" ] && ip link set eth0 mtu "$MTU" 2>/dev/null
ip addr add "$IP/24" dev eth0 2>/dev/null
ip route add default via 192.168.64.1 2>/dev/null

# dados do Docker: data.img do host, direto como virtio-blk (nanoc.datadev) ou,
# no modo antigo (NANOC_DATA_BLK=0), por loop sobre o share orbdata. Sempre ext4.
DATA=$(sed -n 's/.*nanoc\.data=\([^ ]*\).*/\1/p' /proc/cmdline)
[ -n "$DATA" ] || DATA=data.img
DATADEV=$(sed -n 's/.*nanoc\.datadev=\([a-z0-9]*\).*/\1/p' /proc/cmdline)
mkdir -p /var/lib/docker "$(dirname "/data/$DATA")"
if [ -n "$DATADEV" ] && [ -b "/dev/$DATADEV" ]; then
  # só formata disco que o host acabou de criar: nunca apaga um existente
  if grep -q nanoc.datanew=1 /proc/cmdline; then
    mkfs.ext4 -q -F "/dev/$DATADEV" > /data/diag.log 2>&1
  fi
  { echo "dev: /dev/$DATADEV"; mount -t ext4 "/dev/$DATADEV" /var/lib/docker 2>&1; } > /data/mount.log 2>&1
else
  { echo "DATA=$DATA"; ls -l "/data" 2>&1 | head -5; command -v mkfs.ext4; } > /data/diag.log 2>&1
  if [ ! -e "/data/$DATA" ]; then
    { truncate -s 8G "/data/$DATA" 2>&1 && mkfs.ext4 -q -F "/data/$DATA" 2>&1; } >> /data/diag.log 2>&1
  fi
  if [ -e "/data/$DATA" ]; then
    { echo "losetup:"; losetup /dev/loop0 "/data/$DATA" 2>&1; echo "mount:"; mount -t ext4 /dev/loop0 /var/lib/docker 2>&1; } > /data/mount.log 2>&1
  fi
fi

# binfmt: imagens linux/amd64 rodam via qemu-x86_64
if [ -x /usr/bin/qemu-x86_64 ]; then
  mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc 2>/dev/null
  if [ -w /proc/sys/fs/binfmt_misc/register ]; then
    echo ':qemu-x86_64:M::\x7f\x45\x4c\x46\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/usr/bin/qemu-x86_64:OCF' > /proc/sys/fs/binfmt_misc/register 2>/dev/null
  fi
fi

printf "nameserver 1.1.1.1\nnameserver 8.8.8.8\n" > /etc/resolv.conf

if ! grep -q nanoc.nodocker /proc/cmdline; then
  # com nanoc.mtu, as redes bridge (a padrão e as novas, como as do compose)
  # usam o mesmo MTU; senão os containers ficam presos a quadros de 1500 bytes
  DOCKERD_CFG=
  if [ -n "$MTU" ]; then
    printf '{"mtu": %s, "default-network-opts": {"bridge": {"com.docker.network.driver.mtu": "%s"}}}\n' \
      "$MTU" "$MTU" > /run/nanoc-dockerd.json
    DOCKERD_CFG="--config-file /run/nanoc-dockerd.json"
  fi
  dockerd --storage-driver=overlay2 $DOCKERD_CFG >/data/dockerd.log 2>&1 &
  i=0
  while [ $i -lt 25 ]; do docker info >/dev/null 2>&1 && break; sleep 1; i=$((i+1)); done
fi

if grep -q nanoc.nodocker /proc/cmdline; then
  MDIR="/data/$(dirname "$DATA")"
  mkdir -p /root/.ssh /etc/ssh
  ssh-keygen -A >/dev/null 2>&1
  if [ -f "$MDIR/ssh/authorized_keys" ]; then
    cp "$MDIR/ssh/authorized_keys" /root/.ssh/authorized_keys
    chmod 600 /root/.ssh/authorized_keys
  fi
  /usr/sbin/sshd -p 2222 -o PermitRootLogin=yes -o PasswordAuthentication=no \
    -o StrictModes=no >/data/sshd.log 2>&1
fi

echo NANO-GUEST-READY

# supervisor: mantém o agente vivo mesmo se ele sair. Roda em primeiro plano:
# se este script terminar, o guestinit (PID 1) desliga a VM.
while true; do
  /opt/nanoc-agent >>/data/agent.log 2>&1
  sleep 1
done
