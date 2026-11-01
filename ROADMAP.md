# Roadmap

The project aims to stay small: a Docker runtime for macOS with no GUI and no
extra daemons.

## Next

- [ ] `nano logs` / `nano exec`-friendly helper commands
- [ ] configurable vCPU count and disk size (`~/.nano/config.json`)
- [ ] `nano doctor` (checks entitlements, assets and socket state)
- [ ] install script and Homebrew tap

## Later

- [ ] vsock instead of TCP for the Docker proxy (needs a kernel with
      `virtio_vsock` built in)
- [ ] LAN port publishing through a privileged `vmnet` helper
- [ ] EROFS rootfs with a custom kernel that ships the module
- [ ] optional `--kubernetes` (k3s), off by default

## Out of scope

- GUI
- Rosetta / foreign-architecture translation
- multi-node or clustered operation
