# Experimental libkrun backend

`nanoc krun-boot` boots the regular nanoc guest (same kernel, initramfs, modloop
and rootfs) on [libkrun](https://github.com/containers/libkrun) instead of
Virtualization.framework. It exists to measure one thing: whether the guest's
freed memory goes back to macOS. It is not wired into `nanoc up` and has no
network (libkrun has no built-in NAT).

## Build

libkrun `main` at `a037532` (2.0.0-dev), with `libkrun-a037532.patch` applied:

- the PL011 node in the FDT gets `arm,primecell`, without which Linux never
  creates `ttyAMA0`;
- 2-byte MMIO writes are handled on macOS (the Linux PL011 driver does them,
  and libkrun panicked with `unsupported mmio len=2`).

```bash
cd libkrun && git apply ../nano-container/build/krun/libkrun-a037532.patch
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

## Not done

Network (gvisor-tap-vsock or gvproxy), `nanoc up` integration, Docker in the
guest (tested with `nanoc.nodocker=1`), long uptime, real container load, and
shipping the Rust dylib through Homebrew. libkrun 2.0 is not released yet.
