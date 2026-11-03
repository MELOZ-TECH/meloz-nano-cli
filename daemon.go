package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/Code-Hex/vz/v3"
)

// runDaemon: dono da VM. Monta a configuração, sobe o guest, publica o socket
// do Docker e o socket de controle (status/stop) do CLI.
// guestIP é configurável para permitir máquinas extras na mesma rede NAT.
func guestIP() string {
	if v := os.Getenv("NANOC_GUEST_IP"); v != "" {
		return v
	}
	return "192.168.64.2"
}

// portas vsock do nanoc-agent no guest
const (
	guestDockerPort  = 2375
	guestControlPort = 2376
	guestRelayPort   = 2377
)

// daemonLock fica aberto enquanto o daemon vive: o flock some junto com o processo.
var daemonLock *os.File

// lockDaemon impede dois daemons no mesmo estado. O socket de controle só existe
// depois do boot, então checar só ele deixava dois `up` próximos subirem duas VMs
// montando o mesmo data.img.
func lockDaemon(ctlPath string) error {
	f, err := os.OpenFile(ctlPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("já existe um daemon usando %s", filepath.Dir(ctlPath))
	}
	daemonLock = f
	return nil
}

// daemonAlive diz se algum processo segura o lock do daemon, ou seja, se há um
// daemon vivo, respondendo ou não.
func daemonAlive(ctlPath string) bool {
	f, err := os.OpenFile(ctlPath+".lock", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
}

func runDaemon(kernel, initrd, share, sockPath, ctlPath string) error {
	if err := lockDaemon(ctlPath); err != nil {
		return err
	}
	ev := eventsFile(filepath.Dir(ctlPath))
	started := time.Now()
	logEvent(ev, "daemon_start", map[string]any{"backend": "vz", "version": version, "mem_max_mb": memBytes() / mib})
	home, _ := os.UserHomeDir()
	modloop := envOr("NANOC_MODLOOP", filepath.Join(filepath.Dir(kernel), "modloop-virt"))
	data := envOr("NANOC_DATA", filepath.Join(home, ".nanoc", "data.img"))

	// init_on_alloc=0: com o padrão do Alpine o kernel zera cada página alocada, e
	// o balão, ao inflar, encostava em páginas que o guest nunca usou (a VM passava
	// a ocupar o teto inteiro no Mac). psi=1 liga /proc/pressure para o controlador
	// de memória.
	cmdline := "console=hvc0 panic=-1 rdinit=/guestinit init_on_alloc=0 psi=1 nanoc.ip=" + guestIP()
	if v := os.Getenv("NANOC_DATA_REL"); v != "" {
		cmdline += " nanoc.data=" + v
	}
	if v := os.Getenv("NANOC_HOSTNAME"); v != "" {
		cmdline += " nanoc.host=" + v
	}
	noDocker := os.Getenv("NANOC_NO_DOCKER") == "1"
	if noDocker {
		cmdline += " nanoc.nodocker=1"
	}
	if tcpTransport() {
		cmdline += " nanoc.tcp=1"
	}

	dataDir := filepath.Dir(data)
	if v := os.Getenv("NANOC_SHARE_DIR"); v != "" {
		dataDir = v
	}
	_ = os.MkdirAll(dataDir, 0o755)
	dataPath := filepath.Join(dataDir, envOr("NANOC_DATA_REL", filepath.Base(data)))
	dataBlk := os.Getenv("NANOC_DATA_BLK") != "0"
	if dataBlk {
		created, err := ensureDataImage(dataPath)
		if err != nil {
			return err
		}
		dev := "vda"
		if _, err := os.Stat(modloop); err == nil {
			dev = "vdb" // o modloop vem antes
		}
		cmdline += " nanoc.datadev=" + dev
		if created {
			cmdline += " nanoc.datanew=1"
		}
	}
	rosetta := rosettaShare()
	if rosetta != nil {
		cmdline += " nanoc.rosetta=1"
	}
	loader, err := vz.NewLinuxBootLoader(kernel,
		vz.WithCommandLine(cmdline),
		vz.WithInitrd(initrd),
	)
	if err != nil {
		return err
	}
	cfg, err := vz.NewVirtualMachineConfiguration(loader, 2, memBytes())
	if err != nil {
		return err
	}

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return err
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return err
	}
	cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		return err
	}
	cfg.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloon})

	sockCfg, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return err
	}
	cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{sockCfg})

	// discos: modloop read-only (vda) e, por padrão, o data.img direto como
	// virtio-blk (vdb). O loop sobre virtiofs fica para NANOC_DATA_BLK=0.
	var disks []vz.StorageDeviceConfiguration
	if _, err := os.Stat(modloop); err == nil {
		att, err := vz.NewDiskImageStorageDeviceAttachment(modloop, true)
		if err != nil {
			return err
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return err
		}
		disks = append(disks, blk)
	}
	if dataBlk {
		blk, err := dataDisk(dataPath)
		if err != nil {
			return err
		}
		disks = append(disks, blk)
	}
	cfg.SetStorageDevicesVirtualMachineConfiguration(disks)

	// share de dados (rootfs.erofs; data.img só no modo loop) — o rootfs é EROFS read-only
	shares := make([]vz.DirectorySharingDeviceConfiguration, 0)
	if dataShare, err := newShare("orbdata", dataDir); err == nil {
		shares = append(shares, dataShare)
	}
	// /Users compartilhado no mesmo caminho, para -v /Users/... funcionar
	if os.Getenv("NANOC_SHARE_USERS") != "0" {
		if _, err := os.Stat("/Users"); err == nil {
			if usersShare, err := newShare("orbusers", "/Users"); err == nil {
				shares = append(shares, usersShare)
			}
		}
	}
	if rosetta != nil {
		shares = append(shares, rosetta)
	}
	cfg.SetDirectorySharingDevicesVirtualMachineConfiguration(shares)

	pr, _, err := os.Pipe()
	if err != nil {
		return err
	}
	serial, err := vz.NewFileHandleSerialPortAttachment(pr, os.Stdout)
	if err != nil {
		return err
	}
	serialCfg, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serial)
	if err != nil {
		return err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialCfg})

	if ok, err := cfg.Validate(); err != nil || !ok {
		return fmt.Errorf("config inválida: %v", err)
	}

	vm, err := startVM(cfg)
	if err != nil {
		return err
	}
	mem := newMemController(vm)

	// espera o guest responder (docker ou agente de controle)
	waitPort := guestDockerPort
	if noDocker {
		waitPort = guestControlPort
	}
	var dialErr error
	for i := 0; i < 90; i++ {
		time.Sleep(time.Second)
		c, err := dialGuest(vm, waitPort)
		if err != nil {
			dialErr = err
			continue
		}
		c.Close()
		dialErr = nil
		break
	}
	if dialErr != nil {
		logEvent(ev, "guest_timeout", map[string]any{"port": waitPort, "error": dialErr.Error()})
		return fmt.Errorf("guest não respondeu na porta %d: %v", waitPort, dialErr)
	}
	logEvent(ev, "guest_ready", map[string]any{"boot_s": int(time.Since(started).Seconds())})

	var ln net.Listener
	if !noDocker {
		_ = os.Remove(sockPath)
		ln, err = net.Listen("unix", sockPath)
		if err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}

	// controle do CLI
	if c, err := net.DialTimeout("unix", ctlPath, 300*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("já existe um daemon em %s", ctlPath)
	}
	_ = os.Remove(ctlPath)
	ctl, err := net.Listen("unix", ctlPath)
	if err != nil {
		return err
	}
	pidPath := ctlPath + ".pid"
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)

	// se a VM parar sozinha, o daemon sai junto: senão o status continua
	// dizendo "rodando" com o socket do Docker morto
	var stopping atomic.Bool
	go func() {
		for st := range vm.StateChangedNotify() {
			if st != vz.VirtualMachineStateStopped && st != vz.VirtualMachineStateError {
				continue
			}
			if !stopping.Load() {
				fmt.Fprintf(os.Stderr, "VM parou inesperadamente (estado %v); daemon saindo\n", st)
			}
			logEvent(ev, "vm_stopped", map[string]any{"state": fmt.Sprint(st), "requested": stopping.Load()})
			ctl.Close()
			_ = os.Remove(ctlPath)
			if ln != nil {
				ln.Close()
				_ = os.Remove(sockPath)
			}
			_ = os.Remove(pidPath)
			if stopping.Load() {
				os.Exit(0)
			}
			os.Exit(1)
		}
	}()

	fmt.Fprintf(os.Stderr, "docker do guest disponível em %s\n", sockPath)
	if mem != nil {
		go mem.run()
	}

	// portas publicadas pelo Docker no guest ficam acessíveis em 127.0.0.1:<porta>
	pf := newPortForward()
	if !noDocker {
		go pf.run(sockPath)
		relay := relayDialer(func() (net.Conn, error) { return dialGuest(vm, guestRelayPort) })
		go runDomains(sockPath, filepath.Dir(ctlPath), ev, pf.dial, relay)
	}

	go func() {
		for {
			c, err := ctl.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req struct {
					Cmd       string   `json:"cmd"`
					Action    string   `json:"action"`
					Container string   `json:"container"`
					Argv      []string `json:"argv"`
					MB        uint64   `json:"mb"`
				}
				_ = json.NewDecoder(c).Decode(&req)
				resp := map[string]any{"running": true, "pid": os.Getpid(), "docker_sock": sockPath, "ports": pf.active(), "bind": listenAddr()}
				for k, v := range mem.status() {
					resp[k] = v
				}
				if req.Cmd == "k8s" {
					_ = json.NewEncoder(c).Encode(guestControl(vm, req.Action))
					return
				}
				if req.Cmd == "balloon" {
					_ = json.NewEncoder(c).Encode(balloonCmd(mem, req.MB))
					return
				}
				if req.Cmd == "run" {
					_ = json.NewEncoder(c).Encode(guestControlRaw(vm, map[string]any{
						"cmd":  "run",
						"argv": req.Argv,
					}))
					return
				}
				if req.Cmd == "exec" {
					_ = json.NewEncoder(c).Encode(guestControlRaw(vm, map[string]any{
						"cmd":       "exec",
						"container": req.Container,
						"argv":      req.Argv,
					}))
					return
				}
				if req.Cmd == "stop" {
					fmt.Fprintf(os.Stderr, "%s parada pedida pelo socket de controle\n", time.Now().Format(time.RFC3339))
					logEvent(ev, "stop_request", peerInfo(c))
					stopping.Store(true)
					resp["stopping"] = true
					if vm.CanRequestStop() {
						_, _ = vm.RequestStop()
					}
					go func() {
						for i := 0; i < 100; i++ {
							if vm.State() != vz.VirtualMachineStateRunning {
								break
							}
							time.Sleep(100 * time.Millisecond)
						}
						_ = os.Remove(pidPath)
						os.Exit(0)
					}()
				}
				_ = json.NewEncoder(c).Encode(resp)
			}(c)
		}
	}()

	// API do Kubernetes: 127.0.0.1:6443 -> guest:6443 (quando habilitado)
	go func() {
		kl, err := net.Listen("tcp", "127.0.0.1:6443")
		if err != nil {
			fmt.Fprintf(os.Stderr, "api k8s indisponível: %v\n", err)
			return
		}
		for {
			kc, err := kl.Accept()
			if err != nil {
				return
			}
			go func(kc net.Conn) {
				defer kc.Close()
				gc, err := net.DialTimeout("tcp", guestIP()+":6443", 5*time.Second)
				if err != nil {
					return
				}
				defer gc.Close()
				go io.Copy(gc, kc)
				io.Copy(kc, gc)
			}(kc)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-stop
		fmt.Fprintf(os.Stderr, "%s parada por sinal %v\n", time.Now().Format(time.RFC3339), sig)
		logEvent(ev, "signal", map[string]any{"signal": sig.String()})
		ctl.Close()
		if ln == nil {
			// máquina sem Docker: o loop principal está parado em select{}
			_ = os.Remove(pidPath)
			os.Exit(0)
		}
		ln.Close()
	}()

	if ln == nil {
		select {}
	}
	for {
		hc, err := ln.Accept()
		if err != nil {
			_ = os.Remove(pidPath)
			return nil
		}
		go func(hc net.Conn) {
			defer hc.Close()
			vc, err := dialGuest(vm, guestDockerPort)
			if err != nil {
				return
			}
			defer vc.Close()
			proxyCopy(hc, vc)
		}(hc)
	}
}

type closeWriter interface{ CloseWrite() error }

// halfCloser devolve quem sabe fechar só a escrita de c. O VirtioSocketConnection
// do vz não expõe CloseWrite, mas o rawConn por baixo é um socket unix do macOS
// que sabe; sem isso o fim do stdin (`echo x | docker exec -i c cat`) nunca
// chegava ao guest e a conexão ficava pendurada para sempre.
func halfCloser(c net.Conn) closeWriter {
	if cw, ok := c.(closeWriter); ok {
		return cw
	}
	v := reflect.ValueOf(c)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	f := v.Elem().FieldByName("rawConn")
	if !f.IsValid() || !f.CanAddr() {
		return nil
	}
	raw := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
	cw, _ := raw.(closeWriter)
	return cw
}

// proxyCopy encaminha nos dois sentidos e propaga o half-close (docker attach depende disso).
func proxyCopy(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw := halfCloser(dst); cw != nil && cw.CloseWrite() == nil {
			return
		}
		// sem half-close: fechar tudo é a única forma de liberar o outro sentido
		_ = dst.Close()
		_ = src.Close()
	}
	go pipe(b, a)
	go pipe(a, b)
	wg.Wait()
}

// tcpTransport: NANOC_VSOCK=0 troca o vsock por TCP na rede NAT (só para depuração:
// o agente fica aberto para qualquer processo do Mac).
func tcpTransport() bool { return os.Getenv("NANOC_VSOCK") == "0" }

// dialGuest conecta numa porta do nanoc-agent. Por vsock, que só o processo dono
// da VM alcança; o agente não escuta TCP a menos que tcpTransport() esteja ligado.
func dialGuest(vm *vz.VirtualMachine, port int) (net.Conn, error) {
	if tcpTransport() {
		return net.DialTimeout("tcp", fmt.Sprintf("%s:%d", guestIP(), port), 5*time.Second)
	}
	devs := vm.SocketDevices()
	if len(devs) == 0 {
		return nil, fmt.Errorf("VM sem dispositivo vsock")
	}
	return devs[0].Connect(uint32(port))
}

// guestControl fala com o nanoc-agent no guest (porta 2376) e devolve a resposta.
func guestControl(vm *vz.VirtualMachine, action string) map[string]any {
	return guestControlRaw(vm, map[string]any{"cmd": "k8s-" + action})
}

func guestControlRaw(vm *vz.VirtualMachine, payload map[string]any) map[string]any {
	out := map[string]any{"ok": false, "message": "guest sem controle"}
	var c net.Conn
	var err error
	for i := 0; i < 15; i++ {
		c, err = dialGuest(vm, guestControlPort)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		out["message"] = err.Error()
		return out
	}
	defer c.Close()
	_ = json.NewEncoder(c).Encode(payload)
	var resp map[string]any
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		out["message"] = err.Error()
		return out
	}
	return resp
}

// rosettaShare: o Rosetta para Linux da Apple como share virtiofs "rosetta", pela
// API pública do Virtualization.framework. O guest registra no binfmt o Rosetta
// no lugar do qemu-x86_64, e imagens amd64 rodam bem mais rápido. Só quando o
// Rosetta está instalado no Mac (softwareupdate --install-rosetta); NANOC_ROSETTA=0
// desliga.
func rosettaShare() vz.DirectorySharingDeviceConfiguration {
	if os.Getenv("NANOC_ROSETTA") == "0" ||
		vz.LinuxRosettaDirectoryShareAvailability() != vz.LinuxRosettaAvailabilityInstalled {
		return nil
	}
	fs, err := vz.NewVirtioFileSystemDeviceConfiguration("rosetta")
	if err != nil {
		return nil
	}
	share, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return nil
	}
	fs.SetDirectoryShare(share)
	return fs
}

func newShare(tag, dir string) (vz.DirectorySharingDeviceConfiguration, error) {
	fs, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
	if err != nil {
		return nil, err
	}
	shared, err := vz.NewSharedDirectory(dir, false)
	if err != nil {
		return nil, err
	}
	one, err := vz.NewSingleDirectoryShare(shared)
	if err != nil {
		return nil, err
	}
	fs.SetDirectoryShare(one)
	return fs, nil
}

// ensureDataImage cria o data.img esparso (dataImageBytes) se ele não existir. O guest só
// formata quando recebe nanoc.datanew=1, para nunca apagar um disco existente.
func ensureDataImage(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return true, f.Truncate(dataImageBytes())
}

// dataImageBytes: tamanho do data.img. O arquivo é esparso e só ocupa no Mac o
// que o guest grava, então o teto pode ser folgado. NANOC_DISK_GB (padrão 64).
func dataImageBytes() int64 {
	gb := 64
	if v, err := strconv.Atoi(os.Getenv("NANOC_DISK_GB")); err == nil && v >= 8 {
		gb = v
	}
	return int64(gb) << 30
}

// growDataImage aumenta o data.img até dataImageBytes se ele for menor; nunca
// diminui. O guest estende o ext4 no boot com resize2fs online.
func growDataImage(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if st.Size() >= dataImageBytes() {
		return false, nil
	}
	return true, os.Truncate(path, dataImageBytes())
}

// startVM cria e liga a VM. O processo da VM anterior ainda segura os discos por
// alguns segundos depois que o daemon sai, e num `nanoc down && nanoc up` o Start
// falhava com "The storage device attachment is invalid". Tenta de novo por até 15 s.
func startVM(cfg *vz.VirtualMachineConfiguration) (*vz.VirtualMachine, error) {
	var err error
	for i := 0; i < 30; i++ {
		var vm *vz.VirtualMachine
		if vm, err = vz.NewVirtualMachine(cfg); err == nil {
			if err = vm.Start(); err == nil {
				return vm, nil
			}
		}
		if i == 0 {
			fmt.Fprintf(os.Stderr, "%s VM não ligou, tentando de novo por 15 s: %v\n",
				time.Now().Format(time.RFC3339), err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, err
}

// dataDisk anexa o data.img como virtio-blk. fsync em vez de F_FULLFSYNC: o mesmo
// compromisso de durabilidade do loop sobre virtiofs, sem a ida e volta do FUSE.
func dataDisk(path string) (*vz.VirtioBlockDeviceConfiguration, error) {
	att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(path, false,
		vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync)
	if err != nil {
		return nil, err
	}
	return vz.NewVirtioBlockDeviceConfiguration(att)
}
