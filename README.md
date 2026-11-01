# nano-container

[![ci](https://github.com/MELOZ-TECH/nano-container/actions/workflows/ci.yml/badge.svg)](https://github.com/MELOZ-TECH/nano-container/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/MELOZ-TECH/nano-container)](https://github.com/MELOZ-TECH/nano-container/releases)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![go](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](go.mod)

A small, CLI-only container runtime for macOS on Apple Silicon.
It boots a minimal Linux VM through **Virtualization.framework**, runs Docker inside it,
and exposes the Docker socket to your Mac — no GUI, no SSH. Kubernetes is optional
and off by default.

> Status: **experimental**.

## Why

Most container runtimes for macOS bring a full Linux distribution, a GUI, an SSH
daemon and a large feature surface. `nano-container` keeps only what `docker run`
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
./nanoc up
export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock

# 4. use it
docker run --rm alpine:3.22 echo hello

# 5. stop
./nanoc down
```

`nanoc status` reports the running daemon. The Docker socket lives at
`~/.nanoc/docker.sock` and the state (btrfs `data.img`, compressed rootfs) at `~/.nanoc/`.

## Commands

| Command | Description |
|---|---|
| `nanoc up` | starts the VM in a detached daemon and waits until Docker answers |
| `nanoc down` | stops the daemon/VM |
| `nanoc status` | shows whether the daemon is running |
| `nanoc ports` | lists ports published on `127.0.0.1` |
| `nanoc k8s enable` | starts k3s inside the guest and writes `~/.nanoc/kubeconfig` |
| `nanoc k8s disable` | stops k3s (state stays on the data image) |
| `nanoc k8s status` | shows whether Kubernetes is running |
| `nanoc k8s reset` | removes the k3s data (use if the cluster is corrupted) |

Environment variables: `NANOC_MEM_MB` (VM memory, default `1024`), `NANOC_ROOTFS`,
`NANOC_DATA`, `NANOC_MODLOOP`, `NANOC_STATE`.

## How it works

```
nanoc CLI ──unix socket──▶ nanoc daemon ──Virtualization.framework──▶ Alpine VM
   (Go)                   (Go)                                        ├─ guestinit (PID 1)
                                                                       ├─ dockerd (overlay2)
                                                                       └─ nanoc-agent ──▶ docker.sock
```

- The guest boots an Alpine kernel with a custom initramfs whose `guestinit`:
  mounts the host share, loads kernel modules from the Alpine **modloop**, mounts the
  compressed rootfs (squashfs) read-only with a tmpfs overlay, mounts the Docker
  data image (btrfs via loop) and starts `dockerd`.
- The daemon proxies `~/.nanoc/docker.sock` to the guest over the NAT network.
- No GUI, no SSH. Kubernetes is opt-in.

### Published ports

`docker run -p 8080:80` works from macOS: the daemon watches the Docker API and
forwards every published port to `127.0.0.1:<port>` on the host.

```sh
docker run -d -p 8080:80 nginx:alpine
open http://localhost:8080
nanoc ports
```

### Kubernetes (optional)

Kubernetes is **off by default**; enabling it starts a single-node k3s inside the
guest (about 300–400 MB extra while running):

```sh
nanoc k8s enable
export KUBECONFIG=$HOME/.nanoc/kubeconfig
kubectl get nodes

nanoc k8s disable
```

The API server is proxied from `127.0.0.1:6443` on the host to the guest, so
kubectl works out of the box. Cluster state lives on the btrfs data image and
survives restarts until `nanoc k8s reset`.

More detail in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). If something fails,
start with [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).

## Tests

```sh
make test    # unit tests (go vet + go test)
make itest   # integration: boots the VM, runs a container, stops (macOS only)
```

GitHub Actions runs build + unit tests. The integration test cannot run on CI:
GitHub's macOS runners are themselves VMs and macOS does not allow nested
virtualization.

## Limitations

- No Rosetta, vmnet/LAN port publishing or GUI. Kubernetes is opt-in.
- Guest rootfs is a compressed squashfs image with a tmpfs overlay. EROFS was the
  first choice, but the Alpine `virt` kernel ships neither EROFS built-in nor as a
  module, so squashfs is used instead.
- Single VM, fixed 2 vCPU; memory via `NANOC_MEM_MB`.
- macOS only: depends on Virtualization.framework, vmnet and codesign.

## Project docs

- [Architecture](docs/ARCHITECTURE.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)
- [Roadmap](ROADMAP.md)
- [Changelog](CHANGELOG.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)

## License

Apache-2.0.
