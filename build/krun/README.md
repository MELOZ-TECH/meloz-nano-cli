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

libkrun `main` at `302d447d` (2.0.0-dev), which has the two macOS fixes this
backend first needed (libkrun/libkrun#971: `arm,primecell` on the PL011 node,
without which Linux never creates `ttyAMA0`, and 2-byte MMIO writes), plus five
patches sent upstream and three kept in the fork:

- `libkrun-vsock-half-close.patch` (libkrun/libkrun#973): the unix vsock proxy
  turns a half-close from the Mac side into a vsock shutdown instead of a reset,
  so the guest can still answer (every `docker exec` depends on it), shuts down
  each direction of the host socket separately, and gives accepted sockets
  `SO_NOSIGPIPE`;
- `libkrun-virtiofs-cache-timeouts.patch` (libkrun/libkrun#974):
  `krun_fs_device_set_cache_timeouts`, used for the /Users cache below;
- `libkrun-hvfgic-wake-wfi.patch` (libkrun/libkrun#975): with Apple's in-kernel
  GIC, a device interrupt now wakes a vCPU parked on WFI (see "Idle guests").
- `libkrun-vsock-nonblocking-send.patch` (libkrun/libkrun#978, on top of #973):
  the unix vsock proxy no longer blocks when the Mac side stops reading. Before,
  `docker export | docker run -i` froze the whole guest for an hour (RCU stalls,
  a CPU spinning on the virtio console), because one full host socket blocked
  the thread that serves every vsock connection and the console.
- `libkrun-net-vnet-hdr.patch` (libkrun/libkrun#979): `NetFlags::VNET_HDR`
  keeps the virtio-net header on the unixgram socket, for the experimental TSO
  (see "TSO").
- `virtiofs-vendor-hvc` (fork branch only, since the protocol is nanoc's):
  virtio-fs answers small requests over a vendor hypercall on the vCPU thread
  (see "/Users requests over a hypercall").
- `balloon-reclaim-64k` (fork branch only): free guest memory goes back to
  macOS in 64 KiB blocks instead of 2 MiB (see "Memory in 64 KiB blocks").
- `virtiofs-xattr-list` (fork branch, not yet sent upstream): before reading
  the `user.containers.override_stat` owner xattr, virtio-fs lists the file's
  xattr names and reads the value only when the key is there. On APFS a
  `getxattr` of a missing name took 11–22 µs, against ~2 µs for the `lstat`
  and 3–4 µs for the `listxattr`, and every lookup paid it. In a container,
  `find` over 10,000 files on /Users went from 290–450 to 99–190 ms and
  `find -exec stat` from 310–450 to 110–220 ms (cold caches, two runs of four
  each, old and new alternating).

They touch the same list of end-to-end tests, so they do not apply one after
another; `libkrun-nanoc-302d447d.patch` is all of them together, as built here
(`make test TEST='*'` gave 20 of 21 before #978 and #979, the failure being the
pre-existing `virtiofs-misc`; with #978, `vsock-*` 7/7 and `tsi-*` 3/3):

```bash
cd libkrun && git checkout 302d447d
git apply ../nano-container/build/krun/libkrun-nanoc-302d447d.patch
cargo build --release -p libkrun --no-default-features --features blk,net,ffi
```

While the pull requests are open, `build/krun/update-libkrun.sh` rebuilds that
patch: it takes libkrun `main` (or `BASE=<commit>`), merges the PR branches from
the fork that are still open and the fork-only branches (`FORK`), skips and reports the ones already merged
upstream, resolves the conflict they always have in the test list, moves the
pinned commit when the base changes, and writes the new patch. It does not
compile; the CI does.

`build/krun/build-libkrun.sh <dir>` does all of that (clone, checkout, patch,
cargo). It needs several minutes and GBs of RAM: on an 8 GB Mac stop the nanoc VM
first and set `CARGO_BUILD_JOBS=2`. The CI and the release build it on GitHub's
macOS runners, and `make release LIBKRUN=<dir>` packs `lib/libkrun.2.dylib` with
an nanoc built with `-tags krun` that finds it through `@executable_path/../lib`,
which is where Homebrew puts it.

To build by hand instead, build nanoc with the `krun` tag, point the binary at the dylib and sign it
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

## Idle guests and the in-kernel GIC (2026-11-03)

`lat.py` times one virtio-fs request at a time from a container. On an idle
nanoc VM a negative lookup took about 1 ms (OrbStack: 12 µs), and the busy dbolt
VM 154 µs. The cause is in libkrun: with Apple's in-kernel GIC (`hv_gic`, used
whenever macOS has it), `HvfGicV3::set_irq` only calls `hv_gic_set_spi`. A vCPU
that trapped WFI sits in `wait_for_event` and nothing wakes it for an SPI, so
the guest sees a device completion only at its next timer deadline.
`libkrun-hvfgic-wake-wfi.patch` wakes the parked vCPUs after raising the SPI.

| per request, idle VM | before | with the patch | OrbStack |
|---|---|---|---|
| negative lookup | 975–1075 µs | 44–73 µs | 12–13 µs |
| create file | 2570–3360 µs | 293 µs | 62–73 µs |
| delete file | 5530–9330 µs | 168–196 µs | 29–38 µs |

`fsbench.sh` on an idle VM went from find/stat 0.27/0.39 s to 0.16/0.17 s, reading
10,000 files from 3.5 to 2.5 s, deleting 2,000 from 1.05 to 0.46 s, and 300 MB
from 0.41/1.01 s (read/write) to 0.27/0.76 s. Waking between vCPUs was already
fine (`ipi.py`: 52 µs round trip, OrbStack 29 µs).

The rest of the gap is the transport itself. OrbStack's guest kernel sends small
synchronous FUSE requests with an `HVC 0xC600E004` from `virtio_fs_enqueue_req`
(interrupts masked, at most 3 in and 2 out arguments, 128 pages) and the host
answers them on the vCPU thread, with no virtqueue, no worker thread and no
interrupt; anything else falls back to the virtqueue.

## /Users requests over a hypercall (2026-11-03)

Experimental, `NANOC_KRUN_FSHVC=1`. A small synchronous virtio-fs request
(lookup, getattr, create, unlink) spent most of its time on the virtqueue round
trip: the queue notification, waking libkrun's worker thread, and the interrupt
back. OrbStack's guest instead sends file requests with an HVC. nanoc now does
the same for these requests:

- `guest/notify/virtiofs-hvc.patch` changes the guest's `virtio_fs.c` (6.18.42).
  With the module parameter `hvc=1`, once the device is online it asks the host
  for an id by its tag (HVC `0xC6000100`). A non-background request that fits in
  16 segments is then described in a table in guest memory and sent with HVC
  `0xC6000101`; the reply is in place when the call returns. If the host refuses
  it before handling it, the request goes through the virtqueue.
- `guest/notify/build.sh` builds that `virtiofs.ko` next to `nanoc_notify.ko`;
  `bootstrap.sh` puts it in the initramfs (the guestinit loads virtiofs from
  there) and `pack-modloop.sh` in the modloop. Without `hvc=1` it behaves like
  the stock module, so the VZ backend is unaffected.
- The daemon adds `virtiofs.hvc=1` to the kernel command line, and the guestinit
  passes it to `modprobe`, which in the initramfs does not read the command
  line.
- libkrun (`virtiofs-vendor-hvc`): hvf answers vendor calls
  `0xC6000100..=0xC60001FF` through a handler a device registers (without one
  they return -1 instead of stopping the VM), and virtio-fs answers them with
  the same server as the queue, on the calling vCPU.

Two things kept it off at first: the query ran before `virtio_device_ready`,
when libkrun had not created the device yet, and libkrun reports PSCI 0.2, so
`arm_smccc_1_1_get_conduit()` says there is no conduit even though HVC works.

`lat.py` on /Users, 2-vCPU guest with 1.5 GB (OrbStack from the earlier run):

| | virtqueue | hypercall | OrbStack |
|---|---|---|---|
| negative lookup | 51 µs | 12 µs | 12 µs |
| create file | 317 µs | 141 µs | 62–73 µs |
| delete file | 197 µs | 104 µs | 29–38 µs |

A 20 MB random file kept its SHA-256 both ways, and 4 processes creating and
reading 1,200 files in parallel gave no errors or kernel warnings. Create and
delete are still about twice OrbStack's; what is left is on the Mac side (the
passthrough's system calls), not in the transport.

## Memory in 64 KiB blocks (2026-11-04)

libkrun's Replace mode only gave back free blocks of 2 MiB. Smaller free pieces
were only `madvise(MADV_FREE)`d, which does not lower the footprint of pages the
guest wrote. Most of a guest's free memory is in those smaller pieces: in a 2 GB
guest, 60 s after a light load (postgres running, a python and a node container
run), 533 MB were free in pieces of 64 KiB to 2 MiB and 81 MB in smaller ones.

- libkrun (`balloon-reclaim-64k`) maps guest RAM in 64 KiB blocks, each its own
  VM object, and replaces whole blocks as before. XNU merges an anonymous
  mapping into the previous one while the result stays under 128 KiB if both
  have the same VM tag, and replacing one block of a merged object would leave
  that part's pages in memory. So neighbouring blocks get alternating tags (240,
  241).
- nanoc adds `page_reporting.page_reporting_order=4` (64 KiB with 4 KiB pages)
  to the kernel command line; `NANOC_KRUN_FPR_ORDER` changes it.

Tested on the Mac with Hypervisor.framework: `madvise` with `MADV_FREE` or
`MADV_DONTNEED`, with or without unmapping the range from the VM first, left the
footprint unchanged; replacing the blocks with a new `mmap` lowered it by
exactly the replaced memory, about 3 µs per 16 KiB block. Each block costs a VM
object and a map entry in the kernel (256 and 64 bytes): about 10 MB per 2 GB of
guest RAM with 64 KiB blocks, 41 MB with 16 KiB. Mapping the RAM in blocks takes
about 10 ms per GB at boot.

Same load, same guest, MEM in `top` 60 s later: 1498 MB with 2 MiB blocks,
641 MB with 64 KiB blocks.

## Data disk (2026-11-03)

`data.img` is created as a 64 GB sparse file (`NANOC_DISK_GB`, 8 before). On the
libkrun backend `up` grows a smaller existing file (it never shrinks one) and the
guest script runs `resize2fs` on the mounted ext4, which needs `e2fsprogs-extra`
in the rootfs. The daemon runs `fstrim /var/lib/docker` every 5 minutes; libkrun's
virtio-blk turns the discards into holes in the file.

Test VM: an 8 GB disk took 7.9 MB on the Mac; after the next `up` the file was
64 GB, the ext4 62.9 GB and an earlier file still there. Writing 1 GB took the
file to 1.0 GB, and deleting it plus `fstrim` brought it back to 18 MB. On the
dbolt VM a first `fstrim` gave back 1.2 GB (5.8 → 4.6 GB).

## TSO (2026-11-03, experimental)

With `NANOC_KRUN_TSO=1` the guest gets `HOST_TSO4/6`: it hands 64 KB TCP
segments to the device even with MTU 1500. libkrun keeps the virtio-net header on
the socket (`NetFlags::VNET_HDR`, patch `libkrun-net-vnet-hdr`), and the gvisor
side reads and writes that socket through `vnetConn`, which strips the header on
read and writes a zeroed one: one syscall per frame, as before. gvisor accepts a
segment larger than the MSS and doesn't verify received checksums. The other
direction still follows the guest's MSS: gvisor would have to declare GSO on its
link for that, which needs a change to gvisor-tap-vsock.

`netbench.sh`, small test VM:

| | MTU 1500 | MTU 1500 + TSO | MTU 65520 (default) |
|---|---|---|---|
| container → Mac | 1.6–1.9 Gbit/s | 7.4–8.9 Gbit/s | 6.3–9.4 Gbit/s |
| Mac → container | 1.9–2.7 Gbit/s | 1.2–3.0 Gbit/s | 2.6–6.3 Gbit/s |
| daemon CPU, same run | 213 s | 201 s | 179 s |

So MTU 65520 stays the default; TSO is for when the guest has to keep MTU 1500.
A first version relayed frames through a goroutine pair and halved Mac →
container (0.6–1.1 Gbit/s); giving gvisor the socket directly fixed that.

## Not done

Building `nanoc_notify.ko` natively in CI (the release job runs it under QEMU), TSO (needs libkrun to pass the virtio-net header over the socket), background
QoS, long uptime, real container load, and shipping the Rust dylib
through Homebrew. libkrun 2.0 is not released yet.
