package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

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

func guestAddr() string        { return guestIP() + ":2375" }
func guestAddrControl() string { return guestIP() + ":2376" }

func runDaemon(kernel, initrd, share, sockPath, ctlPath string) error {
	home, _ := os.UserHomeDir()
	modloop := envOr("NANOC_MODLOOP", filepath.Join(filepath.Dir(kernel), "modloop-virt"))
	data := envOr("NANOC_DATA", filepath.Join(home, ".nanoc", "data.img"))

	cmdline := "console=hvc0 panic=-1 rdinit=/guestinit nanoc.ip=" + guestIP()
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

	// disco read-only com os módulos do kernel (modloop)
	if _, err := os.Stat(modloop); err == nil {
		att, err := vz.NewDiskImageStorageDeviceAttachment(modloop, true)
		if err != nil {
			return err
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return err
		}
		cfg.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blk})
	}

	// share de dados (rootfs.erofs + data.img) — o rootfs é EROFS read-only
	shares := make([]vz.DirectorySharingDeviceConfiguration, 0)
	dataDir := filepath.Dir(data)
	if v := os.Getenv("NANOC_SHARE_DIR"); v != "" {
		dataDir = v
	}
	_ = os.MkdirAll(dataDir, 0o755)
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

	vm, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return err
	}
	if err := vm.Start(); err != nil {
		return err
	}

	// espera o guest responder (docker ou agente de controle)
	waitAddr := guestAddr()
	if noDocker {
		waitAddr = guestAddrControl()
	}
	var dialErr error
	for i := 0; i < 90; i++ {
		time.Sleep(time.Second)
		c, err := net.DialTimeout("tcp", waitAddr, time.Second)
		if err != nil {
			dialErr = err
			continue
		}
		c.Close()
		dialErr = nil
		break
	}
	if dialErr != nil {
		return fmt.Errorf("guest não respondeu em %s: %v", waitAddr, dialErr)
	}

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

	fmt.Fprintf(os.Stderr, "docker do guest disponível em %s\n", sockPath)

	// portas publicadas pelo Docker no guest ficam acessíveis em 127.0.0.1:<porta>
	pf := newPortForward()
	if !noDocker {
		go pf.run(sockPath)
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
				}
				_ = json.NewDecoder(c).Decode(&req)
				resp := map[string]any{"running": true, "pid": os.Getpid(), "docker_sock": sockPath, "ports": pf.active(), "bind": listenAddr()}
				if req.Cmd == "k8s" {
					_ = json.NewEncoder(c).Encode(guestControl(req.Action))
					return
				}
				if req.Cmd == "run" {
					_ = json.NewEncoder(c).Encode(guestControlRaw(map[string]any{
						"cmd":  "run",
						"argv": req.Argv,
					}))
					return
				}
				if req.Cmd == "exec" {
					_ = json.NewEncoder(c).Encode(guestControlRaw(map[string]any{
						"cmd":       "exec",
						"container": req.Container,
						"argv":      req.Argv,
					}))
					return
				}
				if req.Cmd == "stop" {
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
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		ln.Close()
		ctl.Close()
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
			vc, err := dialGuest(vm, 2375)
			if err != nil {
				return
			}
			defer vc.Close()
			proxyCopy(hc, vc)
		}(hc)
	}
}

// proxyCopy encaminha nos dois sentidos e propaga o half-close (docker attach depende disso).
func proxyCopy(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		if cw, ok := b.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			// sem half-close: dá um tempo curto para o outro sentido drenar
			_ = a.SetReadDeadline(time.Now().Add(2 * time.Second))
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		if cw, ok := a.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
		}
	}()
	wg.Wait()
}

// dialGuest tenta vsock (se o kernel do guest suportar) e cai para TCP.
var vsockOnce sync.Once

func dialGuest(vm *vz.VirtualMachine, port int) (net.Conn, error) {
	if os.Getenv("NANOC_VSOCK") == "0" {
		return net.DialTimeout("tcp", fmt.Sprintf("%s:%d", guestIP(), port), 5*time.Second)
	}
	if devs := vm.SocketDevices(); len(devs) > 0 {
		if c, err := devs[0].Connect(uint32(port)); err == nil {
			vsockOnce.Do(func() { fmt.Fprintln(os.Stderr, "transporte do docker: vsock") })
			return c, nil
		}
	}
	return net.DialTimeout("tcp", fmt.Sprintf("%s:%d", guestIP(), port), 5*time.Second)
}

// guestControl fala com o nanoc-agent no guest (:2376) e devolve a resposta.
func guestControl(action string) map[string]any {
	return guestControlRaw(map[string]any{"cmd": "k8s-" + action})
}

func guestControlRaw(payload map[string]any) map[string]any {
	out := map[string]any{"ok": false, "message": "guest sem controle"}
	var c net.Conn
	var err error
	for i := 0; i < 15; i++ {
		c, err = net.DialTimeout("tcp", guestAddrControl(), 2*time.Second)
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
