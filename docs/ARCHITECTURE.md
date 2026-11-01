# Architecture

How `nano-container` boots a Linux VM on macOS, runs Docker inside it and exposes
the Docker socket to the host.

## Components

| Piece | Language | Role |
|---|---|---|
| `nanoc` (CLI) | Go | `up`, `down`, `status`; talks to the daemon over a unix socket |
| daemon (same binary, `nanoc daemon`) | Go | owns the VM, injects nothing, publishes the Docker socket |
| `guestinit` | Go (linux/arm64, static) | PID 1 inside the guest: mounts shares, loads modules, builds the root |
| `nanoc-agent` | Go (linux/arm64, static) | listens on TCP inside the guest and forwards to `/var/run/docker.sock` |
| guest rootfs | Alpine | Docker Engine + containerd + runc, no systemd, no sshd |
| kernel | Alpine `virt` | boots with `console=hvc0 rdinit=/guestinit` |

## Boot flow

```
1. nanoc up
     └─ spawns `nanoc daemon` (detached) and waits for the control socket

2. daemon builds the VM configuration
     ├─ LinuxBootLoader(kernel, initramfs) + cmdline `rdinit=/guestinit`
     ├─ 2 vCPU, NANOC_MEM_MB (default 1 GiB)
     ├─ virtio-net with NAT
     ├─ virtio-balloon
     ├─ virtio-vsock (device present; the Alpine kernel has no vsock module)
     ├─ virtio-blk read-only: Alpine modloop (kernel modules)
     └─ virtiofs share "orbdata": host directory with rootfs.sqfs + data.img

3. guestinit (PID 1)
     ├─ mount /proc /sys /dev
     ├─ modprobe squashfs, virtiofs, virtio_net, overlay, loop
     ├─ mount share orbdata at /data
     ├─ mount modloop (squashfs) and point /lib/modules at it
     ├─ modprobe btrfs, bridge, veth, br_netfilter, nf_tables, iptables…
     ├─ losetup /data/rootfs.sqfs → /ro (squashfs, read-only)
     ├─ tmpfs /rw + overlay(lower=/ro, upper=/rw/upper) → /newroot
     ├─ bind /data into /newroot/data, mount pseudo-fs inside
     └─ chroot /newroot and exec /opt/nanoc-guest.sh

4. guest boot script
     ├─ network: eth0 up, 192.168.64.2/24, default route 192.168.64.1
     ├─ losetup /data/data.img → mount btrfs at /var/lib/docker
     ├─ dockerd --storage-driver=overlay2
     └─ nanoc-agent on :2375

5. daemon exposes the Docker API
     └─ unix socket ~/.nanoc/docker.sock → TCP 192.168.64.2:2375 → docker.sock
```

## Storage

| Data | Where | Format |
|---|---|---|
| Kernel | `assets/Image` | raw arm64 `Image` extracted from the Alpine EFI zboot |
| Initramfs | `assets/initramfs.cpio.gz` | gzip cpio with `guestinit` |
| Modules | `assets/modloop-virt` | squashfs, attached read-only |
| Guest rootfs | `~/.nanoc/rootfs.sqfs` | squashfs + zstd (269 MB dir → 68 MB) |
| Docker data | `~/.nanoc/data.img` | btrfs image, mounted via loop |

Writes to the guest rootfs go to a tmpfs overlay; they are intentionally
ephemeral. Durable state (images, containers, volumes) lives in the btrfs image.

## Networking

- The VM uses Virtualization.framework NAT: guest `192.168.64.2`, host gateway
  `192.168.64.1`.
- Inside the guest, Docker creates `docker0` + iptables rules, so
  `docker run -p 8080:80` publishes on the *guest's* loopback/eth0.
- The host reaches the Docker API only through the daemon's unix socket.
  Ports published by containers are not exposed on the macOS host yet
  (that requires a root helper with the `vmnet` entitlement).

## Memory

- Idle guest: ~92 MB used (measured with `free -m` at 2 GiB).
- Host daemon RSS: ~9-10 MB.
- Boot to a working Docker socket: ~6 s.

## Design decisions

- **Own PID 1 instead of a distro init.** No systemd, no cloud-init, no SSH
  daemon; the boot path is explicit and small.
- **Compressed read-only rootfs + overlay.** Image is ~4× smaller than the
  directory and the guest cannot corrupt its own base system.
- **Data image on the host, mounted by loop.** Avoids reattach problems with
  block devices and keeps a single sparse file to back up.
- **Modules from the Alpine modloop.** The kernel itself stays small; netfilter,
  btrfs and bridge modules are mounted from a signed squashfs only when needed.
- **TCP for the Docker proxy.** The Alpine kernel ships no `virtio_vsock` module,
  so vsock cannot be used without a custom kernel.

## Known limitations

- EROFS is not available in the Alpine `virt` kernel (neither built-in nor as a
  module), hence squashfs.
- No LAN port publishing, no Kubernetes, no Rosetta, no GUI.
- Single VM, fixed 2 vCPU.
- The guest IP is static; there is no DHCP client.
