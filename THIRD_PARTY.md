# Third-party components

`nano-container` itself is Apache-2.0. The released `nanoc-assets-*.tar.gz`
bundles unmodified binaries and userspace from other projects; this file lists
them, their licenses and where to get the corresponding source.

| Component | Version | License | Used for | Source |
|---|---|---|---|---|
| [Code-Hex/vz](https://github.com/Code-Hex/vz) | v3.8.0 | MIT | Virtualization.framework bindings | github.com/Code-Hex/vz |
| [Code-Hex/go-infinity-channel](https://github.com/Code-Hex/go-infinity-channel) | v1.0.0 | MIT | channel helper used by vz | github.com/Code-Hex/go-infinity-channel |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | v0.49.0 | BSD-3-Clause | syscalls, vsock, setns | cs.opensource.google/go/x/sys |
| [golang.org/x/mod](https://pkg.go.dev/golang.org/x/mod) | v0.22.0 | BSD-3-Clause | module tooling (indirect) | cs.opensource.google/go/x/mod |
| [libkrun](https://github.com/libkrun/libkrun) (`lib/libkrun.2.dylib`) | `main` at the commit in `build/krun/build-libkrun.sh`, **modified** | Apache-2.0 | libkrun backend VMM | github.com/libkrun/libkrun; changes in `build/krun/libkrun-nanoc-*.patch` |
| [gvisor-tap-vsock](https://github.com/containers/gvisor-tap-vsock) | v0.9.0 | Apache-2.0 | network of the libkrun backend | github.com/containers/gvisor-tap-vsock |
| [gVisor](https://gvisor.dev) (netstack) | used by gvisor-tap-vsock | Apache-2.0 | userspace TCP/IP stack | github.com/google/gvisor |
| [Linux kernel](https://kernel.org) (`Image`, modules) | 6.18.x-virt (Alpine edge) | GPL-2.0 | guest kernel | git.kernel.org; packaged by Alpine |
| [Alpine Linux](https://alpinelinux.org) userspace | 3.22 | mixed (GPL-2.0, LGPL-2.1, MIT, BSD) | guest rootfs (EROFS) | gitlab.alpinelinux.org/alpine/aports |
| [BusyBox](https://busybox.net) | Alpine 3.22 | GPL-2.0 | initramfs tools, `modprobe`, `losetup` | busybox.net |
| [mdev/initramfs](https://gitlab.alpinelinux.org/alpine/aports) | Alpine 3.22 | GPL-2.0 | early boot | Alpine aports |
| [Docker Engine](https://github.com/moby/moby) + [containerd](https://github.com/containerd/containerd) | Alpine packages | Apache-2.0 | container runtime inside the guest | moby/moby, containerd/containerd |
| [Btrfs-progs](https://btrfs.wiki.kernel.org) | Alpine 3.22 | GPL-2.0 | data image tooling (legacy) | kernel.org/pub/linux/kernel/people/kdave/btrfs-progs |
| [e2fsprogs](https://e2fsprogs.sourceforge.net) | Alpine 3.22 | GPL-2.0 / LGPL-2.0 | ext4 data image | e2fsprogs.sourceforge.net |
| [QEMU](https://www.qemu.org) (`qemu-x86_64`) | Alpine 3.22 | GPL-2.0 | `linux/amd64` emulation via binfmt | qemu.org |
| [OpenSSH](https://www.openssh.com) | Alpine 3.22 | BSD | `nanoc machine ssh` | openssh.com |
| [k3s](https://github.com/k3s-io/k3s) | latest arm64 (downloaded at runtime) | Apache-2.0 | optional Kubernetes | github.com/k3s-io/k3s |
| [squashfs-tools](https://github.com/plougher/squashfs-tools) | build-time only | GPL-2.0 | rootfs/modloop packaging | github.com/plougher/squashfs-tools |
| [erofs-utils](https://git.kernel.org/pub/scm/linux/kernel/git/xiang/erofs-utils.git) | build-time only | GPL-2.0 | EROFS rootfs | git.kernel.org/.../erofs-utils |

## libkrun

Releases bundle `lib/libkrun.2.dylib`, built by `build/krun/build-libkrun.sh`
from libkrun `main` at a fixed commit with `build/krun/libkrun-nanoc-<commit>.patch`
applied. The patch is the nanoc changes to libkrun, all sent upstream as pull
requests (see `build/krun/README.md`), so the shipped library is a modified
version of libkrun. Its license is in the release tarball as
`licenses/libkrun-LICENSE`.

## GPL compliance

The release assets contain GPL-2.0 binaries (kernel, BusyBox, e2fsprogs,
qemu, squashfs tools output). The corresponding source is available from the
upstream projects listed above, and the exact build is reproducible with this
repository's scripts:

```sh
./scripts/bootstrap.sh          # fetches Alpine sources, builds kernel assets,
                                # initramfs, EROFS rootfs and module image
```

Alpine package sources: <https://gitlab.alpinelinux.org/alpine/aports>.
Kernel source: <https://cdn.kernel.org/pub/linux/kernel/v6.x/>.

If you redistribute the assets, keep this file and the upstream license texts.
