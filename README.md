# meloz-nano-cli

A small, CLI-only container runtime for macOS on Apple Silicon.
It runs a minimal Linux VM through **Virtualization.framework**, runs Docker inside it,
and exposes the Docker socket to your Mac — no GUI, no Kubernetes, no SSH.

> Status: **experimental**. Built as a lightweight study of how OrbStack works.

## Why

OrbStack is fast and light, but it ships a GUI, Kubernetes, Rosetta and a lot of surface
we do not need for plain `docker run`. `meloz-nano-cli` keeps the essential architecture
(one tiny VM + one daemon + a thin CLI) and drops the rest:

| | meloz-nano-cli | OrbStack 2.2.5 | Colima 0.10.3 |
|---|---|---|---|
| GUI | none | SwiftUI | none |
| VM RAM | 1 GiB (default) | 4.18 GB | 2 GB |
| Guest | Alpine + own init + squashfs rootfs | Alpine + own init | Ubuntu + systemd |
| Docker data | btrfs loop image | btrfs volume | ext4 |
| Host RSS | ~10 MB | ~279 MB | ~23 MB |
| Boot to Docker | ~6 s | not measured | ~40 s |

## Requirements

- Apple Silicon, macOS 14+
- Go 1.22+
- Docker CLI (`docker`) on the host
- A container runtime to build the guest rootfs (OrbStack, Docker Desktop, Colima)
- Xcode command line tools (for `codesign`)

## Quick start

```sh
# 1. fetch kernel/initramfs/modloop and build the guest rootfs
make bootstrap

# 2. build and sign (the virtualization entitlement is mandatory)
make sign

# 3. start the VM
./nano up
export DOCKER_HOST=unix://$HOME/.nano/docker.sock

# 4. use it
docker run --rm alpine:3.22 echo hello

# 5. stop
./nano down
```

`nano status` reports the running daemon; the Docker socket lives at
`~/.nano/docker.sock` and the state (btrfs `data.img`) at `~/.nano/`.

## Commands

| Command | Description |
|---|---|
| `nano up` | starts the VM in a detached daemon and waits until Docker answers |
| `nano down` | stops the daemon/VM |
| `nano status` | shows whether the daemon is running |

## How it works

```
nano CLI ──unix socket──▶ nano daemon ──VZ──▶ Alpine VM (1 GiB)
   (Go)                   (Go)                  ├─ own init (guestinit)
                                                 ├─ dockerd (overlay2)
                                                 └─ agent ──TCP──▶ docker.sock proxy
```

- The guest boots from an Alpine kernel + a custom initramfs whose `guestinit` mounts
  the rootfs over **virtiofs**, loads kernel modules from the Alpine **modloop**,
  mounts the Docker data image (**btrfs via loop**) and starts `dockerd`.
- The daemon proxies `~/.nano/docker.sock` to the guest over the NAT network.
- No GUI, no SSH, no Kubernetes.

More detail in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Tests

```sh
make test    # unit tests (go vet + go test)
make itest   # integration: boots the VM, runs a container, stops (macOS only)
```

GitHub Actions runs build + unit tests. The integration test cannot run on CI:
GitHub's macOS runners are themselves VMs and macOS does not allow nested
virtualization.

## Limitations

- No Kubernetes, Rosetta, vmnet/LAN port publishing, or GUI.
- Guest rootfs is a compressed squashfs image with a tmpfs overlay. EROFS was the
  first choice, but the Alpine `virt` kernel ships neither EROFS built-in nor as a
  module, so squashfs is used instead.
- Single VM, fixed 2 vCPU; memory via `NANO_MEM_MB`.
- macOS only: depends on Virtualization.framework, vmnet and codesign.

## License

Apache-2.0.
