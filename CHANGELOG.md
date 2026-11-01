# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-01

### Added

- `nano up`, `nano down`, `nano status` and `nano --version`.
- Detached daemon that owns the VM and publishes `~/.nano/docker.sock`.
- Custom guest init (`guestinit`) as PID 1: mounts the host share, loads kernel
  modules from the Alpine modloop, mounts a compressed squashfs rootfs with a
  tmpfs overlay and starts Docker.
- Docker data on a btrfs image (loop device), `overlay2` storage driver.
- Bridge networking with published ports inside the guest.
- `nano-agent` forwarding the guest Docker socket to the host.
- `make bootstrap` to fetch kernel/initramfs/modloop and build the guest rootfs.
- Unit tests, integration test script, CI workflow and architecture docs.

[Unreleased]: https://github.com/MELOZ-TECH/meloz-nano-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/MELOZ-TECH/meloz-nano-cli/releases/tag/v0.1.0
