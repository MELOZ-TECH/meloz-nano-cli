# Experimental libkrun backend

`nanoc krun-boot` boots the regular nanoc guest (same kernel, initramfs, modloop
and rootfs) on [libkrun](https://github.com/containers/libkrun) instead of
Virtualization.framework. It exists to measure one thing: whether the guest's
freed memory goes back to macOS.

With a `-tags krun` build, `NANOC_BACKEND=krun nanoc up` runs the regular daemon
on libkrun: same state directory, sockets and control protocol, so `status`,
`ports`, `down` and `exec` work unchanged and `status` prints `backend:
libkrun`. There is no balloon controller; the guest gets the whole ceiling
(`NANOC_MEM_MB`, half the Mac's RAM by default) and free page reporting gives
back what it frees. The daemon drops the guest's page cache every 45 s, as
OrbStack's vinit does, since cached file pages are not free memory and would
otherwise never return to macOS.

libkrun has no built-in NAT, so `krun-boot` embeds
[gvisor-tap-vsock](https://github.com/containers/gvisor-tap-vsock): a datagram
socketpair carries raw Ethernet frames between libkrun's virtio-net and the
userspace network stack. The guest keeps the same static `192.168.64.2`, with
the gateway at `.1` and the Mac's `127.0.0.1` reachable at `.254`. Ports that
Docker publishes in the guest are forwarded to `127.0.0.1` on the Mac through
the gvisor stack, since the guest IP does not exist outside it. `-nonet` turns
it off.

## Build

libkrun `main` at `a037532` (2.0.0-dev), with `libkrun-a037532.patch` applied:

- the PL011 node in the FDT gets `arm,primecell`, without which Linux never
  creates `ttyAMA0`;
- 2-byte MMIO writes are handled on macOS (the Linux PL011 driver does them,
  and libkrun panicked with `unsupported mmio len=2`).

and with `libkrun-vsock-half-close.patch`: the unix vsock proxy turns a
half-close from the Mac side into a vsock shutdown instead of a reset, so the
guest can still answer (every `docker exec` depends on it), and accepted sockets
get `SO_NOSIGPIPE`.

```bash
cd libkrun && git apply ../nano-container/build/krun/libkrun-a037532.patch
git am ../nano-container/build/krun/libkrun-vsock-half-close.patch
cargo build --release -p libkrun --no-default-features --features blk,net,ffi
```

Then build nanoc with the `krun` tag, point the binary at the dylib and sign it
with `build/krun.entitlements` (`com.apple.security.hypervisor`):

```bash
CGO_CFLAGS=-I$LIBKRUN/include CGO_LDFLAGS=-L$LIBKRUN/target/release \
  go build -tags krun -o nanoc-krun .
cp $LIBKRUN/target/release/libkrun.dylib libkrun.2.dylib
install_name_tool -id @rpath/libkrun.2.dylib libkrun.2.dylib
install_name_tool -change libkrun.2.dylib @executable_path/libkrun.2.dylib nanoc-krun
codesign --force --sign - libkrun.2.dylib
codesign --force --sign - --entitlements build/krun.entitlements nanoc-krun
```

The guest also needs the `guestinit` from this branch, which loads
`virtio_mmio`. Stop the regular nanoc VM first: never run two VMs on an 8 GB Mac.

## Result (2026-11-02, M-series Mac, 8 GB, 2 GB guest)

`measure.py` writes 1.2 GB of random data to a tmpfs in the guest, deletes it
and samples the VM process's `MEM` in `top` (RSS and `footprint` miss the
guest memory, which macOS compresses or accounts elsewhere).

| Step | Replace | Default (`madvise`) | No balloon |
|---|---|---|---|
| Idle | 275 MB | 276 MB | 277 MB |
| 1.2 GB in tmpfs | 1260 MB | 1263 MB | 1267 MB |
| 5 s after delete | 763 MB | 1259 MB | 1268 MB |
| 15 s after delete | 290 MB | 1260 MB | 1268 MB |
| 30 s after delete | 290 MB | 1260 MB | 1268 MB |

Only `KRUN_FREE_PAGE_REPORTING_MODE_REPLACE` gives the memory back. With
Virtualization.framework (current nanoc) the memory the guest has touched never
returns.

## Network (2026-11-02)

`net-test.py` boots with Docker on and checks, from inside the guest unless noted:

| Check | Result |
|---|---|
| Address and default route | `192.168.64.2/24` via `.1` |
| DNS (`nslookup example.com`) | ok |
| HTTP and HTTPS (`wget`) | ok |
| Mac's `127.0.0.1` via `.254` | ok |
| 50 MB download | ok, 57 s (about 7 Mbit/s) |
| dockerd 28.3.3, `docker pull nginx:alpine` | ok |
| `docker run -p 8080:80`, then `http://127.0.0.1:8080` on the Mac | ok |
| A container reaching the internet | ok |

The download is slow. The test runs the VM under `taskpolicy -b` (background
QoS, to spare an 8 GB Mac) and the virtio-net device has no offloads (features
0, MTU 1500), so the number is not a ceiling yet.

## `nanoc up` (2026-11-02)

`up-test.sh` drives the CLI with `NANOC_BACKEND=krun` and a test `data.img`
(`NANOC_DATA`), without background QoS:

| Check | Result |
|---|---|
| `nanoc up` / `nanoc down` | 6 s / 4 s |
| `status`, `ports`, port forward to `127.0.0.1:8080` | ok |
| `docker exec` without stdin | ok |
| piped stdin (`hc-test.sh`): `echo x \| docker exec -i`, empty stdin, 8 MB in and back (SHA-256 equal), `docker run -i`, `tar` over stdin | ok |
| `-v /Users:/Users` in a container | ok |
| 50 MB download in a container | 3 s |
| VM `MEM` with the 7 dbolt containers that started on it, 60 s idle | 1622 MB (the VZ daemon held 1772 MB) |

Without the half-close patch, unpatched libkrun resets the connection when the
Mac side closes its write side, and the response is lost: every `docker exec`
prints nothing.

## Not done

Sending the half-close fix upstream, network throughput (offloads, a larger MTU, no
background QoS), long uptime, real container load, and shipping the Rust dylib
through Homebrew. libkrun 2.0 is not released yet.
