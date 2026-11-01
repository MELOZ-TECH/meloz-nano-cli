# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
