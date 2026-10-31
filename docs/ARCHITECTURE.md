# OrbStack — arquitetura observada (REA, 2026-10-31)

Alvo: `/Applications/OrbStack.app` versão **2.2.5** (build 21124), bundle `dev.kdrag0n.MacVirt`.
Evidência principal: strings/símbolos dos binários + config em `~/.orbstack` + medição no engine vivo.
Nada aqui é código-fonte original: é observação de artefato distribuído.

## 1. Componentes

| Binário | Stack | Papel |
|---|---|---|
| `MacOS/OrbStack` (34 MB) | Swift (SwiftUI, AppKit, Metal) | UI; fala XPC com o Helper |
| `Frameworks/OrbStack Helper.app/.../OrbStack Helper` (70 MB) | Go (`macvirt/vmgr`) + Rust (`vmm`, `brnet`) | daemon: VM, rede, NFS, K8s, Docker |
| `MacOS/scli` (14 MB) | Go (`macvirt/scon`) | CLI `orb`/`orbctl`; JSON-RPC |
| `Resources/assets/release/arm64/kernel` (31 MB) | Linux 7.2.12-orbstack | kernel do guest |
| `.../rootfs.img` (410 MB) | — | rootfs mínimo com containerd/dockerd/k3s |

Pacotes Go vistos: `vmgr/{vmm,rsvm,vnet,drm,conf,util}`, `scon/{cmd,wormclient,sclient}`.

## 2. Boot / VM

- Virtualização: **Virtualization.framework** (`VZVirtualMachine`, `VZVirtualMachineDelegate`,
  `VZVirtioSocketDevice`).
- Estrutura de spec no Helper: `VzSpec` com 26 campos —
  `cpus, memory, kernel, kernelCsmap, cmdline, initrd, console, mtu, macAddressPrefix,
  networkNat, networkGvisor, networkSwift, rng, diskRootfs, diskData, diskSwap, balloon,
  vsock, virtiofs, rosetta, nestedVirt, sound, usb, cacheTopology, nfsInfo`.
- **cmdline do kernel** (string literal no Helper):

  ```
  reboot=k panic=-1 panic_print=0 nomodules console=hvc0 rootfstype=virtiofs rw
  no-kvmapf tsi_hijack virtio_fs virtio_rng virtio_balloon orb-stdin orb-stdout
  virtue-stdin virtue-stdout virtue-stderr vsock
  ```

  Ou seja: **rootfs montado por virtiofs** (não por disco ext4), console em `hvc0`,
  balloon para devolver memória ao macOS, vsock para controle.
- Discos: `data.img.raw` (228 GB **esparso**) e `swap.img` (1 GB).
- Estado/config: `~/.orbstack/vmconfig.json` → `{"cpu": 4}`; memória não é fixada aí.

## 3. Medições no sistema vivo

| Medida | Valor |
|---|---|
| Memória do engine Docker (VM) | **3.893 GiB** |
| vCPUs da VM | **4** |
| Kernel do guest | `7.2.12-orbstack-00394-gbecdd6130068` |
| RSS `OrbStack Helper` | ~209 MB |
| RSS app `OrbStack` | ~6 MB |

## 4. Por que é mais leve que Colima (hipótese sustentada pela evidência)

1. **Sem QEMU**: usa Virtualization.framework direto; a VM é gerenciada pelo macOS.
2. **Guest mínimo**: boot direto do kernel, rootfs por virtiofs, `nomodules`, sem
   systemd/cloud-init/sshd (não há strings de systemd no rootfs; init é próprio).
3. **Balloon**: `virtio_balloon` devolve páginas ao host sob pressão — em Mac de 8 GB
   isso evita swap global.
4. **Rede no host**: `vmnet`/`brnet` (Rust) + `networkNat`/`networkGvisor`; sem NAT
   duplo de VM genérica.
5. **Compartilhamento**: virtiofs/NFS (`nfs.sock`), não sshfs/9p.
6. **x86**: Rosetta (`rosetta` no spec, `verify_rosetta`), não emulação QEMU.
7. **Overhead de host**: um daemon (209 MB) + app (6 MB); Colima soma processo Lima,
   SSH, e um Ubuntu completo por cima do QEMU/vz.

## 5. O que o PoC `meloz-nano-cli` precisa reproduzir (mínimo)

- CLI em **Go** com bindings de Virtualization.framework (cgo/Objective-C ou lib `vz`).
- `VZLinuxBootLoader` com kernel + cmdline acima (sem initrd, rootfs em virtiofs).
- Um share virtiofs com rootfs próprio (Alpine/Debian mínimo) e outro com o workspace.
- `VZVirtioSocketDevice` para controle (exec, sinais) — substitui SSH.
- `VZVirtioTraditionalMemoryBalloonDevice` para devolver memória.
- `VZNATNetworkDeviceAttachment` para rede.
- Dentro do guest: `dockerd` + `containerd` + `containerd-shim-runc-v2`, socket
  publicado ao host por vsock proxy (equivalente ao `~/.orbstack/run/docker.sock`).
- Meta de memória: VM de 2 GB, RSS de host < 300 MB, sem GUI.

## 6. Próximos passos

1. Validar bindings Go (`Code-Hex/vz` ou equivalente) compilando um "hello VM" que só
   dá boot e imprime no console.
2. Construir rootfs mínimo com dockerd (Alpine + `docker` package) em imagem ext4 ou
   diretório virtiofs.
3. Implementar vsock exec + proxy do `docker.sock`.
4. Medir RSS/VM e comparar com o OrbStack (3.893 GiB / 209 MB) e com Colima.

## 7. Estado do PoC (2026-10-31)

`meloz-nano-cli` (Go) já sobe uma VM real e roda Docker:

| Item | Valor |
|---|---|
| Hypervisor | Virtualization.framework via `github.com/Code-Hex/vz/v3` |
| Entitlement | `com.apple.security.virtualization` (assinatura ad-hoc) |
| vCPU / RAM | 2 / 1 GiB |
| Kernel | Alpine `vmlinuz-virt` → `Image` raw extraído do EFI zboot |
| Rootfs | diretório Alpine 3.22 via **virtiofs** (`tag=orbroot`) |
| Rede | NAT da Apple; DHCP entregou `192.168.64.2` |
| Docker | `dockerd` 28.3.3, storage `vfs`, cgroup2, iptables off |
| Prova | `docker run --rm hello-world` → "Hello from Docker!" |
| Boot até shell | ~5 s |

Como rodar:

```sh
cd ~/meloz-nano-cli && go build -o nano . && codesign --force --sign - \
  --entitlements virt.entitlements nano
./nano boot assets/Image assets/initramfs-virt - 80 "$PWD/alpine-root"
# no console: mount -t virtiofs orbroot /mnt; chroot /mnt /bin/sh /opt/test-docker.sh
```

Pendências: proxy do `docker.sock` para o host, exec por vsock (sem shell),
medir RSS vs OrbStack/Colima, e init próprio (hoje o script é manual).

## 8. Segundo marco: docker.sock no host (modo `serve`)

`nano serve <kernel> <initramfs> <rootfs-dir> <socket>` sobe a VM, injeta o
script de boot pelo console, espera o guest e publica:

```
/tmp/nano-docker.sock  →  TCP 192.168.64.2:2375  →  /var/run/docker.sock (guest)
```

Verificado com o CLI do host:

```
$ DOCKER_HOST=unix:///tmp/nano-docker.sock docker version
client=29.8.2 server=28.3.3
$ docker run --rm hello-world
Hello from Docker!
```

### Comparação de consumo (medida agora)

| | meloz-nano-cli | OrbStack |
|---|---|---|
| vCPU | 2 | 4 |
| RAM do guest | **1.0 GiB** | 3.9 GiB |
| RSS no host | **9 MB** | ~215 MB (Helper 209 + app 6) |
| GUI | nenhuma | SwiftUI |
| Recursos | Docker apenas | Docker, K8s, NFS, Rosetta, USB… |

Observação: vsock não funciona com o kernel/initramfs do Alpine (sem
`virtio_vsock.ko`); o proxy usa TCP na rede NAT da Apple. Para vsock seria preciso
um kernel com o módulo (o do OrbStack tem).

## 9. Comparação justa com Colima (2 vCPU / 2 GiB nos dois)

| | meloz-nano-cli | Colima 0.10.3 |
|---|---|---|
| Hypervisor | Virtualization.framework | Virtualization.framework |
| Guest | Alpine 3.22 (kernel 6.12 virt) | Ubuntu (kernel 6.8 generic) |
| Init | script próprio | systemd + cloud-init |
| RAM total no guest | 1973 MB | 1958 MB |
| **RAM usada no guest (parado)** | **92 MB** | **213 MB** |
| **RSS no host** | **9 MB** | **23 MB** (limactl) |
| Boot até docker pronto | **28 s** | ~40 s (com imagem em cache) |
| Docker | 28.3.3 (vfs) | engine do Colima |

Conclusão observada: com a mesma RAM alocada, o meloz-nano-cli mantém ~2,3× menos
memória ocupada dentro do guest e ~2,5× menos no host — por guest mínimo
(sem systemd/cloud-init) e daemon único, o mesmo motivo que deixa o OrbStack
mais leve que uma VM genérica.

Reproduzir:

```sh
colima start --cpu 2 --memory 2 --disk 20
colima ssh -- free -m
ORBLITE_MEM_MB=2048 ./nano serve assets/Image assets/initramfs-virt "$PWD/alpine-root" /tmp/nano-docker.sock
```

## 10. meloz-nano-cli × Colima × OrbStack (medido na mesma máquina)

| | meloz-nano-cli | Colima 0.10.3 | OrbStack 2.2.5 |
|---|---|---|---|
| vCPU | 2 | 2 | 4 |
| RAM total do guest | **1.0 GB** (default) / 2.0 GB | 1.96 GB | **4.18 GB** |
| RAM usada no guest (parado) | **92 MB** (a 2 GB) | **213 MB** | não medido |
| RSS total no host | **9 MB** | **23 MB** | **279 MB** |
| Init do guest | script | systemd + cloud-init | init próprio |
| GUI | não | não | sim (SwiftUI) |
| Extras | Docker | Docker, k3s opcional | Docker, K8s, NFS, Rosetta, USB |

OrbStack medido com os containers `dbolt-*`/`intelicondos-db` rodando; por isso
o número de host é o pior caso dele nesta máquina.

**Resumo:** com a mesma RAM alocada, o meloz-nano-cli fica ~2,3× abaixo do Colima em uso
no guest e ~2,5× em host; contra o OrbStack, usa 1/4 da RAM de VM e ~1/30 do
RSS de host — ao custo de entregar só Docker (sem K8s/NFS/Rosetta/GUI).

## 11. O papel da GUI (SwiftUI) — é frontend?

Evidência coletada no sistema vivo:

| Fato | Medida |
|---|---|
| Quem mapeia `Virtualization.framework` em runtime | **só o Helper** (`lsof` = 1); o app = 0 |
| Entitlements do app | `com.apple.security.virtualization` (não usado em runtime) |
| Entitlements do Helper | `virtualization` + **`com.apple.vm.networking`** (vmnet) |
| Entitlements do `scli` | nenhum de virtualização |
| Strings do app | `xpcClient`, `SMAppService`, `NSStatusItem`, `dev.orbstack.vmgr.private.*` |
| `VZVirtualMachineView` no app | **0** — a GUI não renderiza a tela da VM |
| CLI sem app | `orb start` / `orb status` / `orb config` funcionam |

Conclusão: a GUI é **frontend** de controle (XPC → Helper) + menu bar + updates
(Sparkle) + login item. O dono da VM, da rede vmnet, dos mounts e do Docker é o
**Helper**; o CLI fala com ele por JSON-RPC (`sconrpc`) e consegue iniciar o
daemon sozinho (strings `OrbStack Helper` / `Contents/MacOS/OrbStack` no `scli`).

Existe ainda um **LaunchDaemon root**: `dev.orbstack.OrbStack.privhelper`
(`/Library/PrivilegedHelperTools/...`) para operações privilegiadas.

### O que o CLI precisa herdar da arquitetura (e a GUI não importa)

1. **Daemon persistente** dono da VM + RPC por socket unix (o `meloz-nano-cli` hoje
   roda em foreground).
2. **Helper root** para vmnet (expor portas na LAN) e montagens do host —
   nosso PoC usa NAT e virtiofs, então ainda não precisa.
3. **Store de config** equivalente ao `~/.orbstack/vmconfig.json`.

### O que pode ser descartado

SwiftUI, menu bar, Sparkle/updates, onboarding, login item, e qualquer
renderização da VM.

## 12. Daemon + CLI (paridade com o OrbStack)

O `meloz-nano-cli` agora tem a mesma separação do OrbStack (CLI ↔ daemon):

```
nano up      # sobe daemon destacado (setsid), espera o socket
nano status  # consulta o daemon
nano down    # pede stop e encerra a VM
```

- Socket de controle: `~/.nano/nano.sock` (JSON: `{"cmd":"status"|"stop"}`).
- Socket do Docker: `~/.nano/docker.sock`.
- Log: `~/.nano/daemon.log`.
- Tempo até pronto: **17,5 s**; teste: `docker run --rm alpine:3.22 echo "meloz-nano-cli daemon ok"`.

Diferença em relação ao OrbStack: ainda não há helper root (vmnet/LAN), o init do
guest é injetado pelo console e o transporte do docker.sock é TCP no NAT (vsock
exigiria kernel com `virtio_vsock`).

## 13. O "segredo" do guest (inspeção do rootfs EROFS)

`rootfs.img` (410 MB) não é ext4: começa com magic **EROFS** (`E2 E1 F5 E0`),
filesystem comprimido somente-leitura. Montado num container privilegiado:

- `/sbin/init -> /bin/busybox`; `/etc/inittab` chama **OpenRC** — é **Alpine**,
  **sem systemd, sem cloud-init, sem sshd** (ao contrário do Ubuntu do Colima).
- `/opt/orbstack-guest/` traz o bridge com o macOS: `macctl` (open, pbcopy,
  osascript, mdfind, code…), `diskclient`, `bin-hiprio`.
- No topo do rootfs: `[rosetta]`, `[qemu-arm64]`, `[qemu-x86_64]`, … → binários
  de tradução por arquitetura.
- Dentro da VM, `/dev/vdb1` é **btrfs** (`subvol=/docker`, `nodatacow`, `discard`)
  para o data do Docker.

### Medições dentro da VM do OrbStack (com 3 containers rodando)

```
MemTotal 4.082.500 kB · MemFree 3.072.012 kB · MemAvailable 3.168.732 kB
→ uso ≈ 892 MB
```

### Por que "parece" mais leve que Colima (correção honesta)

| | OrbStack (com 3 DBs) | Colima (parado) | meloz-nano-cli (parado) |
|---|---|---|---|
| RAM total VM | 4,18 GB | 1,96 GB | 1–2 GB |
| Uso no guest | ~892 MB | ~213 MB | ~92 MB |
| RSS host | 279 MB | 23 MB | 9 MB |

Em RAM bruta o Colima *parece* menor — mas ele estava ocioso e o OrbStack tinha
3 bancos rodando. O ganho real do OrbStack é arquitetural: guest Alpine mínimo,
rootfs EROFS comprimido, btrfs para o Docker, virtiofs no lugar de sshfs, balloon,
sem SSH (vsock), boot em segundos.

## 14. Inventário open source usado pelo OrbStack

### Guest (Alpine 3.25-alpha, rootfs EROFS)
musl 1.2.6 · busybox 1.38 · eudev · chrony · dnsmasq 2.93 · nftables 1.1.1 /
iptables 1.8.13 · btrfs-progs 6.17 · squashfs-tools · lz4/zstd/xz · libseccomp ·
fuse3 · lxc-libs 7.0 · kmod · **QEMU user-mode 11.1.2** (arm, i386, mips64el,
ppc64le, riscv64, s390x).

### Container stack
Docker Engine + containerd + runc em `/opt/docker-rootfs`; **BuildKit**
(`moby/buildkit v0.22`); CLI `docker`/`compose`/`buildx` em `xbin/docker-tools`;
`kubectl`; k3s (strings no rootfs/Helper).

### Host Go (Helper 70 MB e `scli`)
`creachadair/jrpc2` (RPC) · `charmbracelet/bubbletea`+`lipgloss` (TUI) ·
`florianl/go-nfqueue` + `mdlayher/netlink` · `gliderlabs/ssh` ·
**`gvisor.dev/gvisor`** (netstack) · `miekg/dns` · `planetscale/vtprotobuf` ·
`grpc`/`protobuf` · `k8s.io/client-go`+`apimachinery` · `moby/buildkit` ·
`opencontainers/image-spec` · `diskfs/go-diskfs` · `getsentry/sentry-go` ·
`keybase/go-keychain` (fork) · `fsnotify` (fork) · `logrus` · `pflag`.

### Host Rust (dentro do Helper)
`tokio 1.45` · `hyper 1.6` · `nix 0.30` · `mio 1.0` · `bytes 1.11`; crates
próprios: **`hvf`** (Hypervisor.framework), **`brnet`** (ponte vmnet),
`devices`, `filemap`, `gruel`, `libvirtue`.

### App Swift
SwiftUI/AppKit (Apple) · **Sparkle 2.8.1** (updates) · Sentry (crash) ·
**swift-nio**, swift-nio-ssl, **swift-crypto**, SwiftProtobuf, gRPC Swift.

### Não é open source
O código do OrbStack (Go/Rust/Swift), a **Rosetta** (Apple) e os frameworks do
macOS.

Licenças: Alpine mistura GPL/LGPL/MIT/BSD; QEMU é GPLv2; Docker/containerd e
Kubernetes são Apache-2.0; libs Go/Rust em sua maioria MIT/BSD/Apache-2.0;
Sparkle e Sentry MIT.

## 15. Rootfs comprimido + overlay (estado final)

O guest não usa mais o diretório do host montado por virtiofs como raiz. Agora:

```
/data/rootfs.sqfs  (68 MB, zstd)   ->  loop1 -> /ro   (squashfs, read-only)
/data/data.img     (8 GB btrfs)    ->  loop0 -> /var/lib/docker
/ro + tmpfs (/rw/upper)            ->  overlay em /newroot -> chroot
```

- `scripts/bootstrap.sh` gera o `rootfs.sqfs` com `mksquashfs -comp zstd`
  (269 MB de diretório → 68 MB de imagem).
- O `guestinit` monta o `modloop` (squashfs), carrega `loop`, `btrfs`, `bridge`,
  `veth` e netfilter, monta o rootfs comprimido e um overlay em tmpfs.
- `/data` é bind-montado dentro do novo root para o `dockerd`, o agente e os logs
  sobreviverem ao `chroot`.

**Por que não EROFS:** o kernel Alpine `virt` (6.12.114-0-virt) não traz EROFS
nem embutido nem como módulo no `modloop` (verificado: não existe `erofs.ko`).
Squashfs entrega o mesmo efeito (imagem comprimida, imutável, com overlay) e já
está disponível no kernel.
