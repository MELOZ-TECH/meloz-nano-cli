# meloz-nano-cli

A small, CLI-only container runtime for macOS on Apple Silicon.
It boots a minimal Linux VM through **Virtualization.framework**, runs Docker inside it,
and exposes the Docker socket to your Mac — no GUI, no Kubernetes, no SSH.

> Status: **experimental**.

## Why

Most container runtimes for macOS bring a full Linux distribution, a GUI, an SSH
daemon and a large feature surface. `meloz-nano-cli` keeps only what `docker run`
needs: a tiny guest, one daemon, one CLI, and a socket.

- VM with 1 GiB of RAM (configurable), 2 vCPU
- Alpine guest with its own PID 1 (`guestinit`), no systemd, no cloud-init, no sshd
- Compressed read-only root filesystem with a writable overlay
- Docker data on a btrfs image, `overlay2` storage driver
- Bridge networking with published ports working inside the VM
- Boot to a usable Docker socket in ~6 seconds

## Requirements

- Apple Silicon, macOS 14+
- Go 1.22+
- Docker CLI (`docker`) on the host
- A Docker-compatible runtime to build the guest rootfs (Docker Desktop, Colima, etc.)
- Xcode command line tools (for `codesign`)

## Quick start

```sh
# 1. fetch the kernel/initramfs/modloop and build the guest rootfs
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

`nano status` reports the running daemon. The Docker socket lives at
`~/.nano/docker.sock` and the state (btrfs `data.img`, compressed rootfs) at `~/.nano/`.

## Commands

| Command | Description |
|---|---|
| `nano up` | starts the VM in a detached daemon and waits until Docker answers |
| `nano down` | stops the daemon/VM |
| `nano status` | shows whether the daemon is running |

Environment variables: `NANO_MEM_MB` (VM memory, default `1024`), `NANO_ROOTFS`,
`NANO_DATA`, `NANO_MODLOOP`, `NANO_STATE`.

## How it works

```
nano CLI ──unix socket──▶ nano daemon ──Virtualization.framework──▶ Alpine VM
   (Go)                   (Go)                                        ├─ guestinit (PID 1)
                                                                       ├─ dockerd (overlay2)
                                                                       └─ nano-agent ──▶ docker.sock
```

- The guest boots an Alpine kernel with a custom initramfs whose `guestinit`:
  mounts the host share, loads kernel modules from the Alpine **modloop**, mounts the
  compressed rootfs (squashfs) read-only with a tmpfs overlay, mounts the Docker
  data image (btrfs via loop) and starts `dockerd`.
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
