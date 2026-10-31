package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/Code-Hex/vz/v3"
)

// runDaemon: dono da VM. Monta a configuração, sobe o guest, publica o socket
// do Docker e o socket de controle (status/stop) do CLI.
func runDaemon(kernel, initrd, share, sockPath, ctlPath string) error {
	home, _ := os.UserHomeDir()
	modloop := envOr("NANO_MODLOOP", filepath.Join(filepath.Dir(kernel), "modloop-virt"))
	data := envOr("NANO_DATA", filepath.Join(home, ".nano", "data.img"))

	loader, err := vz.NewLinuxBootLoader(kernel,
		vz.WithCommandLine("console=hvc0 panic=-1 rdinit=/guestinit"),
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
	_ = os.MkdirAll(filepath.Dir(data), 0o755)
	if dataShare, err := newShare("orbdata", filepath.Dir(data)); err == nil {
		shares = append(shares, dataShare)
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

	// espera o docker do guest responder (NAT: 192.168.64.2)
	const guestAddr = "192.168.64.2:2375"
	var dialErr error
	for i := 0; i < 90; i++ {
		time.Sleep(time.Second)
		c, err := net.DialTimeout("tcp", guestAddr, time.Second)
		if err != nil {
			dialErr = err
			continue
		}
		c.Close()
		dialErr = nil
		break
	}
	if dialErr != nil {
		return fmt.Errorf("guest não respondeu em %s: %v", guestAddr, dialErr)
	}

	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
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

	go func() {
		for {
			c, err := ctl.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req struct {
					Cmd string `json:"cmd"`
				}
				_ = json.NewDecoder(c).Decode(&req)
				resp := map[string]any{"running": true, "pid": os.Getpid(), "docker_sock": sockPath}
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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		ln.Close()
		ctl.Close()
	}()

	for {
		hc, err := ln.Accept()
		if err != nil {
			_ = os.Remove(pidPath)
			return nil
		}
		go func(hc net.Conn) {
			defer hc.Close()
			vc, err := net.Dial("tcp", guestAddr)
			if err != nil {
				return
			}
			defer vc.Close()
			go io.Copy(vc, hc)
			io.Copy(hc, vc)
		}(hc)
	}
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
