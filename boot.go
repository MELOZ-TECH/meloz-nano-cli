package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/Code-Hex/vz/v3"
)

// boot: sobe uma VM Linux (kernel direto, rootfs opcional em disco virtio-blk)
// e escreve o console em stdout. Prova o caminho de boot do meloz-nano-cli.
//
//	nano boot <kernel> <initrd|-> <disk|-> [segundos]
func boot(kernel, initrd, disk, share string, seconds int) error {
	cmdline := "console=hvc0 panic=-1 root=/dev/vda rootfstype=ext4 rw init=/bin/sh"
	opts := []vz.LinuxBootLoaderOption{vz.WithCommandLine(cmdline)}
	if initrd != "-" {
		opts = append(opts, vz.WithInitrd(initrd))
	}

	loader, err := vz.NewLinuxBootLoader(kernel, opts...)
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

	if disk != "" && disk != "-" {
		att, err := vz.NewDiskImageStorageDeviceAttachment(disk, false)
		if err != nil {
			return err
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return err
		}
		cfg.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blk})
	}

	if share != "" && share != "-" {
		fs, err := vz.NewVirtioFileSystemDeviceConfiguration("orbroot")
		if err != nil {
			return err
		}
		dir, err := vz.NewSharedDirectory(share, false)
		if err != nil {
			return err
		}
		one, err := vz.NewSingleDirectoryShare(dir)
		if err != nil {
			return err
		}
		fs.SetDirectoryShare(one)
		cfg.SetDirectorySharingDevicesVirtualMachineConfiguration(
			[]vz.DirectorySharingDeviceConfiguration{fs})
	}

	serial, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout)
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
	fmt.Fprintf(os.Stderr, "VM iniciada (2 vCPU, 1 GiB, disk=%s)\n", disk)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	select {
	case <-stop:
	case <-time.After(time.Duration(seconds) * time.Second):
	}

	if vm.CanRequestStop() {
		_, _ = vm.RequestStop()
	}
	return nil
}
