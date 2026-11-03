# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `~/.nanoc/events.log`: one JSON line per daemon event (`daemon_start`,
  `guest_ready`, `stop_request`, `signal`, `vm_stopped`, `daemon_exit`). A stop
  request records the process that sent it and its parent, read from the
  control socket (`LOCAL_PEERPID`).
- `nanoc up` starts the daemon under a small watcher (`nanoc watch`). A daemon
  cannot log its own `SIGKILL`; the watcher logs the exit code or signal and,
  when the exit was abnormal, memory, swap, the largest processes and the ones
  started in the last two minutes.
- `nanoc events [n]` prints the last events.
- `nanoc mcp`: an MCP server over stdio with `status`, `events`, `daemon_log`,
  `host_snapshot` and `guest_run`, so an agent can debug nanoc from data.

## [0.16.1] - 2026-11-02

### Fixed

- `nanoc up` no longer kills a running daemon that is slow to answer. It sent
  `SIGKILL` to the PID in `nanoc.sock.pid` whenever `status` took more than
  500 ms (a busy Mac, or a VM still booting), taking every container down with
  no line in `daemon.log`. It now checks the daemon lock: if a daemon holds it,
  `up` waits for it; if not, the stale PID file is removed without signalling
  a PID that may belong to another process.

### Upgrade

Only the `nanoc` binary changed: `brew upgrade nano-container`, then
`nanoc down && nanoc up` to run the new daemon. No `nanoc bootstrap` needed.

## [0.16.0] - 2026-11-02

### Changed

- The guest is tuned to touch less memory and compress what goes cold, since
  pages it touches never go back to macOS: zram swap (lz4, up to the guest RAM),
  `vm.swappiness=100`, `vm.page-cluster=0`, `vm.overcommit_memory=1`, no
  watermark boost or proactive compaction, MGLRU `min_ttl_ms=500`, and 2 MB
  transparent huge pages only on `madvise` (16 KB folios stay automatic). Based
  on the settings OrbStack applies in its own guest. `nanoc.nozram` on the
  kernel command line turns zram off.
- The initramfs is Alpine's own plus `guestinit` (29 MB → 10 MB gzip); the other
  kernel modules come from the modloop disk, which is now also visible after the
  switch to the real root, so the guest can load modules later.
- `guestinit` deletes the initramfs before switching root, like busybox
  `switch_root`. It is a RAM filesystem and stayed allocated for the life of the
  VM (97 MB before this release).

### Fixed

- `nanoc down && nanoc up` could fail with "The storage device attachment is
  invalid": the previous VM process still held `data.img` for a few seconds. The
  daemon now retries for up to 15 s.

## [0.15.0] - 2026-11-01

### Added

- Dynamic memory: the VM is created with a ceiling (`NANOC_MEM_MB`, default half
  the Mac's RAM, 2–8 GB) and the memory balloon holds the guest at 1 GB
  (`NANOC_MEM_MIN_MB`). The daemon grows it by 512 MB when the guest runs short
  and shrinks it after a minute of slack. `nanoc status` shows the current size.
  macOS does not get touched memory back until `nanoc down` (the
  Virtualization.framework balloon has no free page reporting).
  `NANOC_MEM_DYNAMIC=0` gives the guest the full ceiling.

### Changed

- The Docker data image is attached as a virtio-blk disk instead of a loop
  device over virtiofs. Reading 256 MB cold went from 2–8 s to 0.15–0.3 s.
  `NANOC_DATA_BLK=0` keeps the old mount.
- The kernel boots with `init_on_alloc=0` and `psi=1`.

### Security

- The guest agent no longer listens on TCP 2375 (Docker API) and 2376 (control,
  including `run`/`exec` as root). Any process on the Mac and any VM on the NAT
  network could reach them. Both are served over vsock only; `NANOC_VSOCK=0`
  turns TCP back on for debugging.

### Fixed

- `scripts/bootstrap.sh` created a btrfs data image, but the guest mounts ext4.
- Port forwarding no longer hardcodes the guest IP.
- Docs said the data image was btrfs; it is ext4.

### Upgrade

Run `nanoc down && nanoc bootstrap && nanoc up`: the agent, guest script and
`guestinit` changed. A data image from 0.14.0 is reused as is.

## [0.14.0] - 2026-11-01

### Fixed

- The VM no longer powers off after one hour: the guest boot script kept the
  agent supervisor in the background and exited after `sleep 3600`.
- The Docker API no longer dies with "too many open files". Closed clients are
  now propagated across vsock (half-close), on both the host daemon and the
  guest agent, and the port watcher reuses one HTTP client.
- `docker exec` finds binaries inside the container: `guestinit` now does a
  real `switch_root` (MS_MOVE + chroot) instead of a plain `chroot`.
- `echo x | docker exec -i c cat` terminates instead of hanging.
- `nanoc status` no longer reports a stopped VM as running: the daemon exits
  when the VM stops and logs why it stopped.
- Two `nanoc up` in a row can no longer boot two VMs on the same `data.img`
  (exclusive lock taken before boot).

### Upgrade

Run `nanoc bootstrap` (or `make bootstrap`) after upgrading: the guest fixes
live in the new kernel/initramfs/rootfs assets.

## [0.13.0] - 2026-11-01

### Added

- `nanoc machine ssh` with an ed25519 keypair generated per machine (sshd on
  port 2222 inside the machine).
- `nanoc doctor` checks the code signature entitlement, assets, rootfs, data
  image, daemon and docker socket.
- CI installs the Homebrew formula and runs `nanoc --version` on macOS.

## [0.12.0] - 2026-11-01

### Changed

- Docker traffic uses **virtio-vsock by default** again. Streaming over vsock
  (attached `docker run`, `logs`, `exec`) now works: both proxy sides drain the
  remaining direction before closing.
- `NANOC_VSOCK=0` forces the TCP transport.

## [0.11.0] - 2026-11-01

### Added

- Guest rootfs is now **EROFS** (compressed, read-only) with a tmpfs overlay,
  falling back to squashfs automatically.
- Default kernel switched to Alpine **edge** (6.18.x-virt) so EROFS and vsock
  modules are available; `ALPINE_CHANNEL=3.22` keeps the stable kernel.
- Docker data image is ext4 (overlay2 friendly).
- `NANOC_VSOCK=1` opts into the virtio-vsock transport (TCP is the default).

## [0.10.0] - 2026-11-01

### Added

- Docker proxy now uses **virtio-vsock** (fallback to TCP); the guest agent
  listens on both transports.
- `scripts/build-kernel.sh` and `scripts/package-kernel.sh` for a future custom
  kernel with EROFS.
- Guest init tries an EROFS rootfs first and falls back to squashfs.

## [0.9.0] - 2026-11-01

### Added

- `nanoc machine` for standalone Linux VMs: `create`, `ls`, `start`, `stop`,
  `rm`, `exec`, `status`.
- Guest hostname and data image path are configurable through the kernel
  command line (`nanoc.host`, `nanoc.data`, `nanoc.ip`).

## [0.8.0] - 2026-11-01

### Added

- `/Users` is shared into the guest, so `docker run -v /Users/...` bind mounts
  work in both directions.
- `linux/amd64` images run via `qemu-x86_64` and `binfmt_misc`.

## [0.7.0] - 2026-11-01

### Added

- `NANOC_LAN=1` binds published ports on `0.0.0.0` (LAN access without a
  privileged helper). `nanoc ports` shows the effective address.

## [0.6.0] - 2026-11-01

### Added

- `nanoc exec` runs commands through the guest agent using `nsenter`, working
  around the broken `docker exec` rootfs resolution in this kernel.

## [0.5.0] - 2026-11-01

### Added

- `nanoc exec` and `nanoc logs` convenience wrappers.
- `scripts/install.sh` signs the downloaded binary with the virtualization
  entitlement.

## [0.4.0] - 2026-11-01

### Added

- Published container ports are forwarded to `127.0.0.1` on the host.
- `nanoc ports` lists the forwarded ports.
- Homebrew formula (`brew install MELOZ-TECH/tap/nano-container`).
- `nanoc bootstrap` downloads the guest assets from the release.

## [0.2.0] - 2026-11-01

### Added

- `nanoc k8s enable|disable|status|reset`: optional single-node k3s inside the
  guest, off by default, with the API proxied to `127.0.0.1:6443`.
- Guest agent now supervises itself and recovers from handler panics.

### Changed

- CLI renamed to `nanoc`, state directory to `~/.nanoc`, env vars to `NANOC_*`.

### Fixed

- Guest hostname is set at boot (k3s node name was `(none)`).
- `vxlan`/`udp_tunnel` modules are loaded from the modloop (flannel failed
  without them).

## [0.1.0] - 2026-11-01

### Added

- `nanoc up`, `nanoc down`, `nanoc status` and `nanoc --version`.
- Detached daemon that owns the VM and publishes `~/.nanoc/docker.sock`.
- Custom guest init (`guestinit`) as PID 1: mounts the host share, loads kernel
  modules from the Alpine modloop, mounts a compressed squashfs rootfs with a
  tmpfs overlay and starts Docker.
- Docker data on a btrfs image (loop device), `overlay2` storage driver.
- Bridge networking with published ports inside the guest.
- `nanoc-agent` forwarding the guest Docker socket to the host.
- `make bootstrap` to fetch kernel/initramfs/modloop and build the guest rootfs.
- Unit tests, integration test script, CI workflow and architecture docs.

[Unreleased]: https://github.com/MELOZ-TECH/nano-container/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/MELOZ-TECH/nano-container/releases/tag/v0.2.0
[0.1.0]: https://github.com/MELOZ-TECH/nano-container/releases/tag/v0.1.0
