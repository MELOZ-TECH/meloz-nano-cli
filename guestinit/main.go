//go:build linux

// guestinit: PID 1 mínimo do meloz-nano-cli. Carrega os módulos do initramfs,
// monta o rootfs por virtiofs, o modloop (módulos extra), o disco btrfs de dados
// e entra no rootfs executando o boot do guest.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: %s: %v\n", what, err)
	}
}

func mount(src, dst, fstype string, flags uintptr, data string) error {
	must(os.MkdirAll(dst, 0o755), "mkdir "+dst)
	fmt.Printf("guestinit: mount %s -> %s (%s)\n", src, dst, fstype)
	return unix.Mount(src, dst, fstype, flags, data)
}

func modprobe(mod string) {
	if err := exec.Command("/sbin/modprobe", mod).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: modprobe %s: %v\n", mod, err)
	}
}

func unameRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func main() {
	fmt.Println("guestinit: start")
	mount("proc", "/proc", "proc", 0, "")
	mount("sysfs", "/sys", "sysfs", 0, "")
	mount("devtmpfs", "/dev", "devtmpfs", 0, "")

	// 1. módulos que já vêm no initramfs
	for _, m := range []string{"squashfs", "virtiofs", "virtio_net", "virtio_pci", "virtio_blk", "overlay"} {
		modprobe(m)
	}

	// 2. rootfs compartilhado pelo host
	if err := mount("orbroot", "/mnt", "virtiofs", 0, ""); err != nil {
		fmt.Fprintln(os.Stderr, "guestinit: sem rootfs virtiofs; seguindo sem chroot")
		poweroff()
	}

	// 3. modloop com os módulos restantes (squashfs em /dev/vda)
	if err := mount("/dev/vda", "/modloop", "squashfs", unix.MS_RDONLY, ""); err == nil {
		if u := unameRelease(); u != "" {
			_ = os.RemoveAll("/lib/modules/" + u)
			_ = os.MkdirAll("/lib/modules", 0o755)
			if err := os.Symlink("/modloop/modules/"+u, "/lib/modules/"+u); err != nil {
				fmt.Fprintf(os.Stderr, "guestinit: symlink modules: %v\n", err)
			}
			for _, m := range []string{"loop", "btrfs", "bridge", "veth", "br_netfilter",
				"nf_tables", "nf_nat", "nf_conntrack", "nf_conntrack_netlink",
				"nft_chain_nat", "nft_compat", "ip_tables", "x_tables",
				"iptable_nat", "iptable_filter", "xt_nat", "xt_MASQUERADE",
				"xt_conntrack", "xt_addrtype", "xt_comment", "xt_mark",
				"xt_multiport", "xt_tcpudp"} {
				modprobe(m)
			}
		}
	}

	// 4. dados do Docker: data.img no share orbdata, montado por loop (btrfs)
	_ = os.MkdirAll("/mnt/var/lib/docker", 0o755)
	mount("orbdata", "/mnt/data", "virtiofs", 0, "")

	// 5. pseudo-fs dentro do novo root
	mount("proc", "/mnt/proc", "proc", 0, "")
	mount("sysfs", "/mnt/sys", "sysfs", 0, "")
	must(unix.Mount("/dev", "/mnt/dev", "", unix.MS_BIND|unix.MS_REC, ""), "bind /dev")
	mount("tmpfs", "/mnt/run", "tmpfs", 0, "")
	mount("tmpfs", "/mnt/tmp", "tmpfs", 0, "")
	mount("cgroup2", "/mnt/sys/fs/cgroup", "cgroup2", 0, "")

	// 6. entra no rootfs
	must(os.Chdir("/mnt"), "chdir /mnt")
	must(syscall.Chroot("/mnt"), "chroot")
	must(os.Chdir("/"), "chdir /")

	fmt.Println("guestinit: executando /opt/nano-guest.sh")
	cmd := exec.Command("/bin/sh", "/opt/nano-guest.sh")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: guest script: %v\n", err)
	}
	poweroff()
}

func poweroff() {
	syscall.Sync()
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
