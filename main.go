package main

import (
	"fmt"
	"os"

	"github.com/Code-Hex/vz/v3"
)

// spike: valida que os bindings de Virtualization.framework compilam e que a
// configuração mínima da VM (kernel direto, NAT, balloon, vsock, console) é aceita.
func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			printVersion()
			return
		case "k8s":
			action := "status"
			if len(os.Args) >= 3 {
				action = os.Args[2]
			}
			if err := k8sCmd(action); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "machine":
			if err := machineCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "doctor":
			if err := doctorCmd(); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "bootstrap":
			if err := bootstrapAssets(); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "exec":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "uso: nanoc exec <container> <cmd...>")
				os.Exit(2)
			}
			if err := execCmd(os.Args[2], os.Args[3:]); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "logs":
			if err := dockerPassthrough(os.Args[1], os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "up", "down", "status", "ports":
			if err := cli(os.Args[1]); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "krun-boot":
			if err := krunBoot(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "watch":
			if err := runWatch(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "events":
			n := 30
			if len(os.Args) >= 3 {
				fmt.Sscanf(os.Args[2], "%d", &n)
			}
			stateDir, _, _ := paths()
			for _, l := range readEvents(eventsFile(stateDir), n, "") {
				fmt.Println(l)
			}
			return
		case "mcp":
			if err := runMCP(os.Stdin, os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "erro:", err)
				os.Exit(1)
			}
			return
		case "daemon":
			run := runDaemon
			if os.Getenv("NANOC_BACKEND") == "krun" {
				run = runKrunDaemon
			}
			if err := run(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]); err != nil {
				panic(err)
			}
			return
		}
	}
	if len(os.Args) < 2 {
		printUsage()
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == "boot" {
		disk := "-"
		if len(os.Args) >= 5 {
			disk = os.Args[4]
		}
		secs := 25
		if len(os.Args) >= 6 {
			fmt.Sscanf(os.Args[5], "%d", &secs)
		}
		share := "-"
		if len(os.Args) >= 7 {
			share = os.Args[6]
		}
		if err := boot(os.Args[2], os.Args[3], disk, share, secs); err != nil {
			panic(err)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Println("uso: nanoc <kernel> | nanoc boot <kernel> <initramfs>")
		os.Exit(2)
	}

	boot, err := vz.NewLinuxBootLoader(os.Args[1],
		vz.WithCommandLine("console=hvc0 panic=-1"),
	)
	if err != nil {
		panic(err)
	}

	cfg, err := vz.NewVirtualMachineConfiguration(boot, 2, 1024*1024*1024)
	if err != nil {
		panic(err)
	}

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		panic(err)
	}
	net, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		panic(err)
	}
	cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{net})

	balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		panic(err)
	}
	cfg.SetMemoryBalloonDevicesVirtualMachineConfiguration(
		[]vz.MemoryBalloonDeviceConfiguration{balloon})

	sock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		panic(err)
	}
	cfg.SetSocketDevicesVirtualMachineConfiguration(
		[]vz.SocketDeviceConfiguration{sock})

	serial, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout)
	if err != nil {
		panic(err)
	}
	serialCfg, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serial)
	if err != nil {
		panic(err)
	}
	cfg.SetSerialPortsVirtualMachineConfiguration(
		[]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialCfg})

	ok, err := cfg.Validate()
	if err != nil {
		panic(err)
	}
	fmt.Printf("config válida=%v cpus=2 mem=1GiB (bindings ok)\n", ok)
}
