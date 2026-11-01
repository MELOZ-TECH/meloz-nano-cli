# Troubleshooting

## `The process doesn't have the "com.apple.security.virtualization" entitlement`

The binary must be signed with the entitlement:

```sh
make sign
```

This creates an ad-hoc signature with `build/virt.entitlements`. Rebuilding the
binary without signing drops the entitlement.

## `guest não respondeu em 192.168.64.2:2375`

The VM booted but Docker did not come up. Check the guest log written to the host
share:

```sh
tail -50 ~/.nano/dockerd.log
tail -50 ~/.nano/daemon.log
```

Common causes:

- `rootfs.sqfs` or `data.img` missing → run `make bootstrap`
- module loading failed → confirm `assets/modloop-virt` exists and matches the
  kernel version
- previous daemon still holding state → `pkill -f "nano daemon"` then `nano up`

## `storage device attachment is invalid`

A previous VM had not fully released the disk image. The daemon retries, but if it
persists:

```sh
pkill -9 -f "nano daemon"
sleep 3
nano up
```

## `docker: Cannot connect to the Docker daemon`

Use the socket exposed by the daemon:

```sh
export DOCKER_HOST=unix://$HOME/.nano/docker.sock
```

## Port publishing does not work from macOS

Ports are published on the guest network only. Reaching them from the host
requires the `vmnet` helper, which is not implemented yet.

## Slow or failed image pulls

The guest resolver is set to `1.1.1.1` and `8.8.8.8`. If your network blocks them,
edit `guest/nano-guest.sh` and rebuild the rootfs (`make bootstrap`).
