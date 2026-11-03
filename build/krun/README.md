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
guest can still answer (every `docker exec` depends on it), shuts down each
direction of the host socket separately, and gives accepted sockets
`SO_NOSIGPIPE`. Sent upstream as libkrun/libkrun#973.

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

## Network throughput

`netbench.sh` runs iperf3 between the Mac and an alpine container, three 5 s
runs per direction (Mbit/s). OrbStack's VMM uses MTU 65535 and a virtio-net
header (`rsvm.monitor.NetworkMTU` returns `0xffff`, `NetworkWantsVnetHdrV1`
returns true; its VZ path uses 1500 and no header). libkrun's unixgram backend
does not carry the header, so the krun backend uses MTU 65520 with checksum
offload and large receive buffers instead (`nanoc.mtu=65520` makes the guest set
`eth0` and every Docker bridge network to that MTU).

| Path | nanoc VZ | krun, MTU 1500 | krun, MTU 65520 | OrbStack |
|---|---|---|---|---|
| Mac → container (published port) | 3077–3377 | 65–196 | 5920–10362 | 2483–5641 |
| container → Mac (published port) | 6335–6832 | 10–24 | 7873–14080 | 944–4209 |
| container → Mac (host address) | 2971–12409 | 32–170 | 10952–14802 | 705–2601 |
| Mac → container (host address) | 2425–2918 | 103–602 | 5275–9558 | 448–1217 |

The VZ run had the 7 dbolt containers running in the same VM; the krun and
OrbStack runs did not. Numbers vary a lot between runs on this 8 GB Mac.

## Mac file changes in containers (2026-11-03)

Without help, a container sees a file the Mac changed only after the virtiofs
cache expires, and inotify inside the container never fires, so hot reload needs
polling. OrbStack solves this with FSEvents on the Mac and its own kernel
(`/dev/krpc`). nanoc does the same with a small module on the stock Alpine kernel:

- `guest/notify/nanoc_notify.c` creates `/dev/nanoc_notify`, which takes lines
  `<c|m|a|d> <path>`. For each one it drops the cached dentry and fires the
  matching fsnotify event (CREATE, MODIFY+CLOSE_WRITE, ATTRIB, DELETE).
  `guest/notify/build.sh` builds it for 6.18.42-0-virt.
- `scripts/bootstrap.sh` builds the module for the kernel in the modloop and adds
  it with `guest/notify/pack-modloop.sh` (`modules/<version>/extra`, `depmod`,
  same xz compression); `NANOC_NOTIFY_MODULE=0` skips it. The build uses
  `linux-virt-dev` when the Alpine repository still has that exact version, and
  otherwise kernel.org plus the aports config of that version, listed in
  `guest/notify/build.sh`. A failure only leaves the stock modloop.
- The daemon runs `modprobe nanoc_notify` after boot. If the modloop lacks it, it
  falls back to `insmod` of `NANOC_KRUN_NOTIFY_KO` (default
  `~/.nanoc/nanoc_notify.ko`, read by the guest through the /Users share), and
  otherwise logs `notify_off`. `NANOC_KRUN_NOTIFY=0` turns it off. It then watches, with FSEvents, only the
  /Users folders that running containers bind-mount (refreshed every 2 s) and
  writes the changes to the device, batched over 20 ms.
- FSEvents keeps old flags for a path (a file created and then edited arrives
  with Created on every later event), so after the first event per path the
  choice between m and a comes from comparing `lstat`, and Created on a file born
  before the watch started is ignored. The stream uses `MarkSelf`, so writes the
  guest made through virtiofs are dropped instead of echoed back.

`fsevents-test.sh` (module from the packed modloop, 68 ms), with /Users on a 5 s cache and no notice injected by hand:
the container read a file grown on the Mac 72–142 ms later (without the module it
read the old content for up to 5 s), and `inotifywait` saw exactly one
MODIFY,CLOSE_WRITE, CREATE, DELETE and ATTRIB, and no echo of its own write. The
kernel is tainted (out-of-tree, unsigned module), with no oops.

### Longer /Users cache with the notices (2026-11-03)

`cache-notify-bench.sh`, test VM: a container repeats `stat` and `cat` over
10,000 files, each pass 1 s after the last; then the Mac makes 9 changes (grow,
same-size rewrite, create, delete, rename old and new name, editor-style save,
chmod, new folder with a file) and the container polls for up to 3 s.

| /Users cache | notices | stat 10k again (ms) | cat 10k again (ms) | changes seen |
|---|---|---|---|---|
| 100 ms | on | 842–1172 | 2665–4979 | 9/9, ≤ 3 ms |
| 100 ms | off | 817–874 | 2564–2943 | 9/9, ≤ 3 ms |
| 5 s | on | 249–823 | 2609–16823 | 9/9, ≤ 232 ms |
| 60 s | on | 210–330 | 2079–3184 | 9/9, ≤ 3 ms |
| 60 s | off | 251–385 | 2120–2619 | 6/9: rename, editor save and chmod still stale after 3 s |

A long cache makes repeated `stat` about 3 times faster; reading content costs
the same, since each open still goes to the Mac. With the notices nothing went
stale. The default is now 10 s when the modloop carries the module (the
`.modloop-notify` marker next to it matches its sha256) and 100 ms otherwise.
10 s caps the worst case for what the notices do not cover: processes outside
containers, the 2 s gap before a new bind mount is watched, and a dropped event.
A run with the new default: 9/9 changes seen within 3 ms. Numbers on this 8 GB
Mac are noisy (the 16.8 s read at 5 s is one outlier).

## Not done

Building `nanoc_notify.ko` natively in CI (the release job runs it under QEMU), TSO (needs libkrun to pass the virtio-net header over the socket), background
QoS, long uptime, real container load, and shipping the Rust dylib
through Homebrew. libkrun 2.0 is not released yet.
