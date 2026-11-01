#!/bin/sh
# Boot do guest: rede, dados (btrfs em loop), dockerd e agente do docker.sock.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

mkdir -p /sys/fs/cgroup /run /tmp
mount -t cgroup2 none /sys/fs/cgroup 2>/dev/null
mount -t tmpfs tmpfs /run 2>/dev/null
mount -t tmpfs tmpfs /tmp 2>/dev/null

ip link set lo up 2>/dev/null
ip link set eth0 up 2>/dev/null
ip addr add 192.168.64.2/24 dev eth0 2>/dev/null
ip route add default via 192.168.64.1 2>/dev/null

# dados do Docker: data.img no host (share orbdata) montado por loop + btrfs
mkdir -p /var/lib/docker
if [ ! -e /data/data.img ]; then
  truncate -s 8G /data/data.img 2>/dev/null && mkfs.btrfs -q -f /data/data.img 2>/dev/null
fi
if [ -e /data/data.img ]; then
  losetup /dev/loop0 /data/data.img 2>/dev/null
  mount -t btrfs /dev/loop0 /var/lib/docker 2>/dev/null
fi

printf "nameserver 1.1.1.1\nnameserver 8.8.8.8\n" > /etc/resolv.conf

dockerd --storage-driver=overlay2 >/data/dockerd.log 2>&1 &
i=0
while [ $i -lt 25 ]; do docker info >/dev/null 2>&1 && break; sleep 1; i=$((i+1)); done

# supervisor: mantém o agente vivo mesmo se ele sair
while true; do
  /opt/nanoc-agent >>/data/agent.log 2>&1
  sleep 1
done &
echo NANO-GUEST-READY
sleep 3600
