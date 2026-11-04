//go:build krun

// Backend experimental: sobe o mesmo guest do nanoc com o libkrun (main, API 2.0)
// em vez do Virtualization.framework, para medir a devolução de memória ao macOS
// pelo free page reporting em modo Replace. O libkrun não traz NAT: a rede é o
// gvisor-tap-vsock embutido, ligado ao virtio-net por um socketpair de datagramas.
// Não está ligado ao `nanoc up`: roda sozinho com `nanoc krun-boot`.
//
// Compilar: CGO_CFLAGS=-I<libkrun>/include CGO_LDFLAGS="-L<libkrun>/target/release
// -Wl,-rpath,<libkrun>/target/release" go build -tags krun, e assinar com
// build/krun.entitlements (com.apple.security.hypervisor).

package main

/*
#cgo LDFLAGS: -lkrun
#include <fcntl.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <libkrun.h>

static char nanoc_krun_err[1024];

static bool nanoc_push(void *self, KrunStr s) {
	size_t n = strlen(nanoc_krun_err);
	size_t room = sizeof(nanoc_krun_err) - n - 1;
	if (s.len < room) room = s.len;
	memcpy(nanoc_krun_err + n, s.data, room);
	nanoc_krun_err[n + room] = 0;
	return true;
}

static KrunPushStrVtable nanoc_push_vt = { .drop = NULL, .push = nanoc_push };

// nanoc_fail guarda a mensagem do libkrun e devolve -1
static int nanoc_fail(const char *what, KrunError err) {
	snprintf(nanoc_krun_err, sizeof(nanoc_krun_err), "%s: ", what);
	if (err) {
		KrunVtableHandle h = KRUN_VTABLE_HANDLE(KRUN_PUSH_STR_TYPE_TAG, nanoc_push_vt, NULL);
		krun_error_message(err, &h);
		krun_error_destroy(err);
	}
	return -1;
}

#define TRY(what, expr) do { err = NULL; expr; if (err) return nanoc_fail(what, err); } while (0)

typedef struct {
	const char *kernel, *initrd, *cmdline;
	const char *modloop, *data, *share, *users;
	const char *sock_docker, *sock_control, *sock_relay;
	int cpus, ram_mib, fpr_mode, log_level, serial;
	int net_fd, users_cache_ms;
	unsigned int net_features, net_flags;
	unsigned char mac[6];
} nanoc_krun_cfg;

// nanoc_krun_run só volta em erro: krun_vmm_run encerra o processo quando o guest desliga
static int nanoc_krun_run(nanoc_krun_cfg *c) {
	KrunError err;
	TRY("log", krun_init_log(STDERR_FILENO, c->log_level, KRUN_LOG_STYLE_AUTO, 0, &err));
	KrunMmioDeviceManager devs = krun_mmio_device_manager_new();

	int devnull = open("/dev/null", O_RDONLY);
	KrunConsoleBuilder cb = krun_console_device_builder();
	TRY("console", krun_console_builder_add_default_console(cb, devnull, STDOUT_FILENO, STDERR_FILENO, &err));
	KrunConsoleDevice con;
	TRY("console", con = krun_console_builder_build(cb, &err));
	krun_mmio_device_manager_add(devs, con);

	// mesma ordem do daemon: modloop vira vda e o data.img, vdb
	if (c->modloop && *c->modloop) {
		KrunBlockDevice blk;
		TRY("modloop", blk = krun_block_device_new(KRUN_STR("modloop"), KRUN_STR(c->modloop), KRUN_DISK_FORMAT_RAW, &err));
		krun_block_device_set_read_only(blk, true);
		krun_mmio_device_manager_add(devs, blk);
	}
	if (c->data && *c->data) {
		KrunBlockDevice blk;
		TRY("data", blk = krun_block_device_new(KRUN_STR("data"), KRUN_STR(c->data), KRUN_DISK_FORMAT_RAW, &err));
		krun_mmio_device_manager_add(devs, blk);
	}

	KrunFsDevice fs;
	TRY("orbdata", fs = krun_fs_device_new(KRUN_STR("orbdata"), KRUN_STR(c->share), &err));
	krun_mmio_device_manager_add(devs, fs);
	// /Users no mesmo caminho, para -v /Users/... funcionar
	if (c->users && *c->users) {
		KrunFsDevice ufs;
		TRY("orbusers", ufs = krun_fs_device_new(KRUN_STR("orbusers"), KRUN_STR(c->users), &err));
		// o Mac edita esses arquivos enquanto o container lê: com o cache padrão
		// de 5 s, um arquivo que cresceu era lido cortado por até 5 s e um salvo
		// por rename (como fazem os editores) continuava velho
		TRY("orbusers cache", krun_fs_device_set_cache_timeouts(ufs, (uint32_t)c->users_cache_ms,
			(uint32_t)c->users_cache_ms, &err));
		krun_mmio_device_manager_add(devs, ufs);
	}

	// tsi_features 0: sem TSI, que exige o kernel do libkrunfw
	KrunVsockDevice vs;
	TRY("vsock", vs = krun_vsock_device_new(3, 0, &err));
	krun_vsock_device_add_unix_port(vs, 2375, KRUN_STR(c->sock_docker), true);
	krun_vsock_device_add_unix_port(vs, 2376, KRUN_STR(c->sock_control), true);
	krun_vsock_device_add_unix_port(vs, 2377, KRUN_STR(c->sock_relay), true);
	krun_mmio_device_manager_add(devs, vs);

	// quadros Ethernet crus, um por datagrama (o protocolo "vfkit" do gvisor-tap-vsock)
	if (c->net_fd >= 0) {
		KrunNetDevice net;
		TRY("net", net = krun_net_device_new_unixgram_fd(KRUN_STR("net0"), c->net_fd,
			KRUN_BYTES(c->mac), c->net_features, c->net_flags, &err));
		krun_mmio_device_manager_add(devs, net);
	}

	if (c->fpr_mode >= 0) {
		KrunBalloonDevice bal;
		TRY("balloon", bal = krun_balloon_device_new(&err));
		krun_balloon_device_set_free_page_reporting_mode(bal, (uint32_t)c->fpr_mode);
		krun_mmio_device_manager_add(devs, bal);
	}
	KrunRngDevice rng;
	TRY("rng", rng = krun_rng_device_new(&err));
	krun_mmio_device_manager_add(devs, rng);

	KrunPayload payload;
	TRY("kernel", payload = krun_payload_load_external(KRUN_STR(c->kernel), KRUN_KERNEL_FORMAT_RAW,
		KRUN_STR(c->initrd), KRUN_STR(c->cmdline), &err));

	KrunVmmBuilder b = krun_vmm_builder_new();
	TRY("vcpus", krun_vmm_builder_vcpus(&b, (uint8_t)c->cpus, &err));
	TRY("ram", krun_vmm_builder_ram_mib(&b, (uint32_t)c->ram_mib, &err));
	krun_vmm_builder_payload(&b, payload);
	krun_vmm_builder_devices(&b, devs);
	// serial PL011 para ver o boot antes do virtio_mmio carregar o hvc0
	// a entrada precisa ser um fd só dela: o libkrun registra cada uma no kqueue
	int sp[2];
	if (c->serial && pipe(sp) == 0) {
		TRY("serial", krun_vmm_builder_add_serial_console(&b, sp[0], STDOUT_FILENO, &err));
	}
	KrunVmm vmm;
	TRY("build", vmm = krun_vmm_builder_build(&b, &err));
	krun_vmm_run(vmm);
	return nanoc_fail("run", NULL);
}

static const char *nanoc_krun_error(void) { return nanoc_krun_err; }
*/
import "C"

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

// MACs fixos: o guest tem IP estático (nanoc.ip) e o gateway é sempre o .1
var (
	krunGuestMAC   = [6]byte{0x5a, 0x94, 0xef, 0xe4, 0x0c, 0xee}
	krunGatewayMAC = "5a:94:ef:e4:0c:dd"
)

// hostAlias é o IP pelo qual o guest alcança o 127.0.0.1 do Mac
const hostAlias = ".254"

// startNetwork sobe a rede virtual na mesma /24 do VZ (o script do guest usa o
// .1 como gateway) e devolve o fd que vai para o virtio-net do libkrun.
// Quadros de até 64 KB entre o guest e o gvisor, como o OrbStack (MTU 65535 no
// VMM dele): com 1500, a rede ficava 20 a 300 vezes abaixo da do VZ. Sem
// NANOC_KRUN_TSO o libkrun não leva o cabeçalho virtio-net pelo socket; em troca a
// placa promete aceitar pacotes grandes (GUEST_TSO4/6, que faz o Linux alocar
// buffers de 64 KB) e não exige checksum na saída (CSUM): o gvisor-tap-vsock não
// confere o checksum do que recebe. NANOC_KRUN_MTU troca o valor (1500 desliga).
const (
	virtioNetFCsum      = 1 << 0
	virtioNetFGuestCsum = 1 << 1
	virtioNetFGuestTSO4 = 1 << 7
	virtioNetFGuestTSO6 = 1 << 8
	virtioNetFHostTSO4  = 1 << 11
	virtioNetFHostTSO6  = 1 << 12

	// krun_net_device_new_unixgram_fd: o cabeçalho virtio-net vai junto de cada
	// quadro no socket (NetFlags::VNET_HDR, patch libkrun-net-vnet-hdr)
	krunNetFlagVnetHdr = 2
)

// krunTSO (NANOC_KRUN_TSO=1, experimental): o guest manda segmentos TCP de até
// 64 KB mesmo com MTU 1500 (HOST_TSO4/6) e o repassador entre o libkrun e o gvisor
// tira o cabeçalho virtio-net antes de entregar. O gvisor aceita segmento maior
// que o MSS e não confere checksum do que recebe. No sentido contrário o gvisor
// segue o MSS do guest: para TSO nos dois sentidos o link dele teria de declarar
// GSO, o que exige mudar o gvisor-tap-vsock.
func krunTSO() bool { return os.Getenv("NANOC_KRUN_TSO") == "1" }

func krunNetFlags() uint32 {
	if krunTSO() {
		return krunNetFlagVnetHdr
	}
	return 0
}

// krunUsersCacheMS: por quanto tempo o guest guarda entradas e atributos de
// /Users. Medido em 10 mil arquivos: com 0 nada fica velho, mas find e stat
// levam de 3,6 a 7 s em vez de 0,2 s; com 100 ms a velocidade é a do cache
// padrão e uma mudança do Mac aparece em até 100 ms. Com o nanoc_notify no
// modloop o Mac avisa as mudanças, e 10 s deixam o stat repetido de 10 mil
// arquivos 3 vezes mais rápido (850 → 300 ms) sem nada velho nos containers;
// o prazo limita o pior caso a quem os avisos não cobrem (processo fora de
// container, aviso perdido). NANOC_KRUN_USERS_CACHE_MS troca o valor.
func krunUsersCacheMS(modloop string) int {
	if v, err := strconv.Atoi(os.Getenv("NANOC_KRUN_USERS_CACHE_MS")); err == nil && v >= 0 {
		return v
	}
	if os.Getenv("NANOC_KRUN_NOTIFY") != "0" && modloopHasNotify(modloop) {
		return 10000
	}
	return 100
}

func krunMTU() int {
	if v, err := strconv.Atoi(os.Getenv("NANOC_KRUN_MTU")); err == nil && v >= 1280 && v <= 65520 {
		return v
	}
	return 65520
}

func krunNetFeatures() uint32 {
	f := uint32(0)
	if krunMTU() > 1500 {
		f |= virtioNetFCsum | virtioNetFGuestCsum | virtioNetFGuestTSO4 | virtioNetFGuestTSO6
	}
	if krunTSO() {
		f |= virtioNetFCsum | virtioNetFHostTSO4 | virtioNetFHostTSO6
	}
	return f
}

func startNetwork() (*virtualnetwork.VirtualNetwork, int, error) {
	ip := guestIP()
	prefix := ip[:strings.LastIndex(ip, ".")]
	vn, err := virtualnetwork.New(&types.Configuration{
		MTU:               krunMTU(),
		Subnet:            prefix + ".0/24",
		GatewayIP:         prefix + ".1",
		GatewayMacAddress: krunGatewayMAC,
		DHCPStaticLeases:  map[string]string{ip: net.HardwareAddr(krunGuestMAC[:]).String()},
		NAT:               map[string]string{prefix + hostAlias: "127.0.0.1"},
		GatewayVirtualIPs: []string{prefix + hostAlias},
		Protocol:          types.VfkitProtocol,
	})
	if err != nil {
		return nil, -1, err
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, -1, err
	}
	// o padrão do macOS (2 KB de envio) perde quadros em rajada
	for _, fd := range fds {
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 2<<20)
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20)
	}
	var gconn net.Conn
	if krunTSO() {
		// o gvisor lê e escreve direto no socket do libkrun, que leva o cabeçalho
		// virtio-net: vnetConn o tira na leitura e põe um zerado na escrita
		gconn = &vnetConn{fd: fds[0]}
	} else {
		f := os.NewFile(uintptr(fds[0]), "gvisor-net")
		conn, err := net.FileConn(f)
		f.Close()
		if err != nil {
			return nil, -1, err
		}
		gconn = conn
	}
	go func() {
		if err := vn.AcceptVfkit(context.Background(), gconn); err != nil {
			fmt.Fprintln(os.Stderr, "nanoc krun-boot: rede:", err)
		}
	}()
	return vn, fds[1], nil
}

// vnetConn é o lado do gvisor de um socketpair cujo outro lado o libkrun usa com
// NetFlags::VNET_HDR: cada datagrama começa com o cabeçalho virtio-net de 12
// bytes. O gvisor só precisa de Read e Write; o resto da interface é vazio.
type vnetConn struct {
	fd   int
	rmu  sync.Mutex
	rbuf []byte
	wmu  sync.Mutex
	wbuf []byte
}

const vnetHdrLen = 12

func (c *vnetConn) Read(b []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if c.rbuf == nil {
		c.rbuf = make([]byte, 128<<10)
	}
	for {
		n, err := syscall.Read(c.fd, c.rbuf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n <= vnetHdrLen {
			continue
		}
		return copy(b, c.rbuf[vnetHdrLen:n]), nil
	}
}

func (c *vnetConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if len(c.wbuf) < vnetHdrLen+len(b) {
		c.wbuf = make([]byte, vnetHdrLen+max(len(b), 64<<10))
	}
	clear(c.wbuf[:vnetHdrLen])
	copy(c.wbuf[vnetHdrLen:], b)
	writeAll(c.fd, c.wbuf[:vnetHdrLen+len(b)])
	return len(b), nil
}

func (c *vnetConn) Close() error                     { return syscall.Close(c.fd) }
func (c *vnetConn) LocalAddr() net.Addr              { return &net.UnixAddr{Net: "unixgram"} }
func (c *vnetConn) RemoteAddr() net.Addr             { return &net.UnixAddr{Net: "unixgram"} }
func (c *vnetConn) SetDeadline(time.Time) error      { return nil }
func (c *vnetConn) SetReadDeadline(time.Time) error  { return nil }
func (c *vnetConn) SetWriteDeadline(time.Time) error { return nil }

// writeAll manda um datagrama, esperando quando o buffer do socket está cheio.
func writeAll(fd int, b []byte) {
	for {
		_, err := syscall.Write(fd, b)
		if err == syscall.ENOBUFS || err == syscall.EAGAIN || err == syscall.EINTR {
			time.Sleep(5 * time.Microsecond)
			continue
		}
		return
	}
}

// krunOpts: o que muda entre o `krun-boot` de teste e o daemon
type krunOpts struct {
	kernel, initrd, modloop, data, share, users string
	sockDocker, sockControl, sockRelay          string
	cmdline                                     string
	cpus, ramMiB, fprMode, logLevel             int
	netFD                                       int // -1: sem rede
	netFeatures                                 uint32
}

// krunRun entra no libkrun na thread atual. Só volta em erro: quando o guest
// desliga, o libkrun encerra o processo.
func krunRun(o krunOpts) error {
	strs := []*C.char{}
	cs := func(s string) *C.char {
		p := C.CString(s)
		strs = append(strs, p)
		return p
	}
	defer func() {
		for _, p := range strs {
			C.free(unsafe.Pointer(p))
		}
	}()
	for _, p := range []string{o.sockDocker, o.sockControl, o.sockRelay} {
		_ = os.Remove(p)
	}
	cfg := C.nanoc_krun_cfg{
		kernel:       cs(o.kernel),
		initrd:       cs(o.initrd),
		cmdline:      cs(o.cmdline),
		modloop:      cs(o.modloop),
		data:         cs(o.data),
		share:        cs(o.share),
		users:        cs(o.users),
		sock_docker:  cs(o.sockDocker),
		sock_control: cs(o.sockControl),
		sock_relay:   cs(o.sockRelay),
		cpus:         C.int(o.cpus),
		ram_mib:      C.int(o.ramMiB),
		fpr_mode:     C.int(o.fprMode),
		log_level:    C.int(o.logLevel),
		// o hvc0 é virtio e só existe depois do modprobe virtio_mmio; sem console no
		// início, o runtime do Go do guestinit não acha os fds 0-2 e sai com código 2.
		// A serial PL011 vem compilada no kernel e vira o console (o último vale).
		serial:         1,
		net_fd:         C.int(o.netFD),
		net_features:   C.uint(o.netFeatures),
		net_flags:      C.uint(krunNetFlags()),
		users_cache_ms: C.int(krunUsersCacheMS(o.modloop)),
	}
	for i, b := range krunGuestMAC {
		cfg.mac[i] = C.uchar(b)
	}
	runtime.LockOSThread()
	C.nanoc_krun_run(&cfg)
	return fmt.Errorf("libkrun: %s", C.GoString(C.nanoc_krun_error()))
}

// krunCmdline: a mesma linha do daemon do VZ, mais a serial como console
func krunCmdline(created, noDocker bool) string {
	cmdline := "console=hvc0 panic=-1 rdinit=/guestinit init_on_alloc=0 psi=1 nanoc.ip=" + guestIP() +
		" nanoc.datadev=vdb"
	if created {
		cmdline += " nanoc.datanew=1"
	}
	if noDocker {
		cmdline += " nanoc.nodocker=1"
	}
	if mtu := krunMTU(); mtu > 1500 {
		cmdline += " nanoc.mtu=" + strconv.Itoa(mtu)
	}
	// NANOC_KRUN_FSHVC=1 (experimental): o virtiofs do guest manda os pedidos
	// pequenos e síncronos por hypercall, que o libkrun do nanoc atende na vCPU
	// (guest/notify/virtiofs-hvc.patch); sem o patch no libkrun, a hypercall
	// derrubaria a VM, por isso só com a biblioteca do nanoc
	if os.Getenv("NANOC_KRUN_FSHVC") == "1" {
		cmdline += " virtiofs.hvc=1"
	}
	return cmdline + " console=ttyAMA0"
}

// krunBoot: nanoc krun-boot -dir <estado> [-ram MiB] [-fpr replace|default|off]
// <estado> precisa ter rootfs.erofs; o data.img é criado ali se faltar. Os sockets
// vsock-2375.sock e vsock-2376.sock ficam em -sockdir e levam às portas do agente.
func krunBoot(args []string) error {
	home, _ := os.UserHomeDir()
	fl := flag.NewFlagSet("krun-boot", flag.ContinueOnError)
	assets := fl.String("assets", filepath.Join(home, ".nano", "assets"), "kernel, initramfs e modloop")
	dir := fl.String("dir", "", "diretório de estado (share orbdata)")
	ram := fl.Int("ram", 2048, "RAM do guest em MiB")
	cpus := fl.Int("cpus", 2, "vCPUs")
	fpr := fl.String("fpr", "replace", "free page reporting: replace, default ou off")
	logLevel := fl.Int("log", 2, "nível de log do libkrun (0-5)")
	noDocker := fl.Bool("nodocker", true, "não sobe o dockerd no guest")
	noNet := fl.Bool("nonet", false, "sem rede (sem gvisor-tap-vsock)")
	// caminho de socket unix no macOS tem limite de 104 bytes
	sockDir := fl.String("sockdir", filepath.Join(home, ".nanoc-krun"), "onde ficam os sockets vsock")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("faltou -dir")
	}
	if _, err := os.Stat(filepath.Join(*dir, "rootfs.erofs")); err != nil {
		return fmt.Errorf("%s sem rootfs.erofs", *dir)
	}
	mode := map[string]int{"replace": 1, "default": 0, "off": -1}
	fprMode, ok := mode[*fpr]
	if !ok {
		return fmt.Errorf("-fpr inválido: %s", *fpr)
	}

	data := filepath.Join(*dir, "data.img")
	created, err := ensureDataImage(data)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(*sockDir, 0o700)
	o := krunOpts{
		kernel:      filepath.Join(*assets, "Image"),
		initrd:      envOr("NANOC_KRUN_INITRD", filepath.Join(*assets, "initramfs.cpio.gz")),
		modloop:     filepath.Join(*assets, "modloop-virt"),
		data:        data,
		share:       *dir,
		sockDocker:  filepath.Join(*sockDir, "vsock-2375.sock"),
		sockControl: filepath.Join(*sockDir, "vsock-2376.sock"),
		sockRelay:   filepath.Join(*sockDir, "vsock-2377.sock"),
		cmdline:     krunCmdline(created, *noDocker),
		cpus:        *cpus,
		ramMiB:      *ram,
		fprMode:     fprMode,
		logLevel:    *logLevel,
		netFD:       -1,
	}
	if !*noNet {
		vn, fd, err := startNetwork()
		if err != nil {
			return fmt.Errorf("rede: %w", err)
		}
		o.netFD = fd
		o.netFeatures = krunNetFeatures()
		// portas publicadas pelo Docker no guest em 127.0.0.1:<porta>, entrando
		// pela pilha do gvisor (o IP do guest não existe fora dela)
		if !*noDocker {
			pf := newPortForward()
			pf.dial = func(addr string) (net.Conn, error) { return vn.Dial("tcp", addr) }
			go pf.run(o.sockDocker)
		}
	}
	fmt.Fprintf(os.Stderr, "nanoc krun-boot: ram=%d MiB fpr=%s pid=%d\n", *ram, *fpr, os.Getpid())
	return krunRun(o)
}
