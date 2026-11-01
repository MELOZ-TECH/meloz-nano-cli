//go:build linux

// guestinit: PID 1 do nano-container. Monta o share de dados, o modloop de
// módulos, o rootfs EROFS read-only com overlay gravável e entra no rootfs.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	host := "nanoc"
	if b, err := os.ReadFile("/proc/cmdline"); err == nil {
		for _, f := range strings.Fields(string(b)) {
			if strings.HasPrefix(f, "nanoc.host=") {
				if v := strings.TrimPrefix(f, "nanoc.host="); v != "" {
					host = v
				}
			}
		}
	}
	must(unix.Sethostname([]byte(host)), "hostname")
	mount("sysfs", "/sys", "sysfs", 0, "")
	mount("devtmpfs", "/dev", "devtmpfs", 0, "")

	for _, m := range []string{"squashfs", "virtiofs", "virtio_net", "virtio_pci", "virtio_blk", "overlay", "loop"} {
		modprobe(m)
	}

	// share de dados: rootfs.erofs + data.img ficam no host
	_ = os.MkdirAll("/users", 0o755)
	if err := mount("orbusers", "/users", "virtiofs", 0, ""); err == nil {
		fmt.Println("guestinit: /Users compartilhado")
	}

	if err := mount("orbdata", "/data", "virtiofs", 0, ""); err != nil {
		fmt.Fprintln(os.Stderr, "guestinit: share orbdata indisponível")
		poweroff()
	}

	// módulos extra vêm do modloop (squashfs read-only em /dev/vda)
	if err := mount("/dev/vda", "/modloop", "squashfs", unix.MS_RDONLY, ""); err == nil {
		if u := unameRelease(); u != "" {
			_ = os.RemoveAll("/lib/modules/" + u)
			_ = os.MkdirAll("/lib/modules", 0o755)
			_ = os.Symlink("/modloop/modules/"+u, "/lib/modules/"+u)
			for _, m := range []string{"loop", "vxlan", "udp_tunnel", "ip6_udp_tunnel", "binfmt_misc", "btrfs", "bridge", "veth",
				"br_netfilter", "nf_tables", "nf_nat", "nf_conntrack",
				"nf_conntrack_netlink", "nft_chain_nat", "nft_compat",
				"ip_tables", "x_tables", "iptable_nat", "iptable_filter",
				"xt_nat", "xt_MASQUERADE", "xt_conntrack", "xt_addrtype",
				"xt_comment", "xt_mark", "xt_multiport", "xt_tcpudp"} {
				modprobe(m)
			}
		}
	}

	// rootfs EROFS read-only + overlay gravável em tmpfs
	if out, err := losetup("-r", "/dev/loop1", "/data/rootfs.sqfs"); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: losetup rootfs: %v %s\n", err, out)
		poweroff()
	}
	if err := mount("/dev/loop1", "/ro", "squashfs", unix.MS_RDONLY, ""); err != nil {
		fmt.Fprintln(os.Stderr, "guestinit: não consegui montar o rootfs squashfs")
		poweroff()
	}
	mount("tmpfs", "/rw", "tmpfs", 0, "")
	_ = os.MkdirAll("/rw/upper", 0o755)
	_ = os.MkdirAll("/rw/work", 0o755)
	_ = os.MkdirAll("/newroot", 0o755)
	if err := unix.Mount("overlay", "/newroot", "overlay", 0,
		"lowerdir=/ro,upperdir=/rw/upper,workdir=/rw/work"); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: overlay: %v\n", err)
		poweroff()
	}

	// pseudo-fs dentro do novo root
	mount("proc", "/newroot/proc", "proc", 0, "")
	mount("sysfs", "/newroot/sys", "sysfs", 0, "")
	must(unix.Mount("/dev", "/newroot/dev", "", unix.MS_BIND|unix.MS_REC, ""), "bind /dev")
	mount("tmpfs", "/newroot/run", "tmpfs", 0, "")
	mount("tmpfs", "/newroot/tmp", "tmpfs", 0, "")
	_ = os.MkdirAll("/newroot/Users", 0o755)
	must(unix.Mount("/users", "/newroot/Users", "", unix.MS_BIND|unix.MS_REC, ""), "bind /Users")
	_ = os.MkdirAll("/newroot/data", 0o755)
	must(unix.Mount("/data", "/newroot/data", "", unix.MS_BIND|unix.MS_REC, ""), "bind /data")
	mount("cgroup2", "/newroot/sys/fs/cgroup", "cgroup2", 0, "")

	must(os.Chdir("/newroot"), "chdir /newroot")
	must(syscall.Chroot("/newroot"), "chroot")
	must(os.Chdir("/"), "chdir /")

	fmt.Println("guestinit: executando /opt/nanoc-guest.sh")
	cmd := exec.Command("/bin/sh", "/opt/nanoc-guest.sh")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: guest script: %v\n", err)
	}
	poweroff()
}

func losetup(args ...string) ([]byte, error) {
	if _, err := os.Stat("/sbin/losetup"); err == nil {
		return exec.Command("/sbin/losetup", args...).CombinedOutput()
	}
	return exec.Command("/bin/busybox", append([]string{"losetup"}, args...)...).CombinedOutput()
}

func poweroff() {
	syscall.Sync()
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
