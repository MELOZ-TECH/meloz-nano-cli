//go:build linux

// guestinit: PID 1 do nano-container. Monta o share de dados, o modloop de
// módulos, o rootfs EROFS read-only com overlay gravável e entra no rootfs.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	out, err := exec.Command("/sbin/modprobe", mod).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: modprobe %s: %v %s\n", mod, err, strings.TrimSpace(string(out)))
		return
	}
	if mod == "virtiofs" || mod == "ext4" {
		fmt.Printf("guestinit: módulo %s carregado\n", mod)
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

	// módulos: o initramfs do Alpine só traz o necessário para chegar aqui; o resto
	// vem do modloop, montado por cima de /lib/modules/<versão>
	modDir := ""
	if u := unameRelease(); u != "" {
		modDir = "/lib/modules/" + u
		if err := mount("/dev/vda", "/modloop", "squashfs", unix.MS_RDONLY, ""); err == nil {
			if _, err := os.Stat("/modloop/modules/" + u); err == nil {
				_ = os.MkdirAll(modDir, 0o755)
				must(unix.Mount("/modloop/modules/"+u, modDir, "", unix.MS_BIND, ""), "bind modloop")
			}
		}
	}
	for _, m := range []string{"virtiofs", "virtio_net", "virtio_pci", "virtio_blk",
		"vsock", "vmw_vsock_virtio_transport", "vmw_vsock_virtio_transport_common",
		"virtio_balloon",
		"loop", "overlay", "vxlan", "udp_tunnel", "ip6_udp_tunnel",
		"binfmt_misc", "ext4", "btrfs", "bridge", "veth", "br_netfilter",
		"nf_tables", "nf_nat", "nf_conntrack", "nf_conntrack_netlink",
		"nft_chain_nat", "nft_compat", "ip_tables", "x_tables",
		"iptable_nat", "iptable_filter", "xt_nat", "xt_MASQUERADE",
		"xt_conntrack", "xt_addrtype", "xt_comment", "xt_mark",
		"xt_multiport", "xt_tcpudp"} {
		modprobe(m)
	}

	// share de dados: rootfs.erofs + data.img ficam no host
	_ = os.MkdirAll("/users", 0o755)
	if err := mount("orbusers", "/users", "virtiofs", 0, ""); err == nil {
		fmt.Println("guestinit: /Users compartilhado")
	}

	if err := mount("orbdata", "/data", "virtiofs", 0, ""); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: share orbdata indisponível: %v\n", err)
		poweroff()
	}

	// rootfs EROFS read-only + overlay gravável em tmpfs
	if err := mountRootfs(); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: rootfs: %v\n", err)
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
	// módulos também no novo root, para o script do guest carregar zram e afins
	if _, err := os.Stat(modDir); modDir != "" && err == nil {
		_ = os.MkdirAll("/newroot"+modDir, 0o755)
		must(unix.Mount(modDir, "/newroot"+modDir, "", unix.MS_BIND, ""), "bind módulos")
	}

	// o initramfs fica na RAM até ser apagado; depois do MS_MOVE ele some de vista
	// mas continua ocupando memória (é o que o switch_root do busybox evita)
	freeInitramfs()

	// switch_root: move o novo root para cima de "/" antes do chroot. Com chroot
	// puro, a raiz do namespace de montagem continua sendo o initramfs, e o
	// `runc exec` (que faz setns e volta para essa raiz) procurava os binários no
	// lugar errado: `docker exec c pg_isready` dava "executable file not found".
	must(os.Chdir("/newroot"), "chdir /newroot")
	must(unix.Mount(".", "/", "", unix.MS_MOVE, ""), "move /newroot -> /")
	must(syscall.Chroot("."), "chroot")
	must(os.Chdir("/"), "chdir /")

	fmt.Println("guestinit: executando /opt/nanoc-guest.sh")
	cmd := exec.Command("/bin/sh", "/opt/nanoc-guest.sh")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "guestinit: guest script: %v\n", err)
	}
	poweroff()
}

// freeInitramfs apaga os arquivos do initramfs, sem descer em outras montagens.
func freeInitramfs() {
	var root unix.Stat_t
	if err := unix.Lstat("/", &root); err != nil {
		return
	}
	var walk func(dir string)
	walk = func(dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			var st unix.Stat_t
			if unix.Lstat(p, &st) != nil || st.Dev != root.Dev {
				continue
			}
			if e.IsDir() {
				walk(p)
				_ = unix.Rmdir(p)
			} else {
				_ = unix.Unlink(p)
			}
		}
	}
	walk("/")
}

// mountRootfs monta o rootfs comprimido: EROFS se o kernel suportar, senão squashfs.
func mountRootfs() error {
	candidates := []struct{ img, fstype string }{
		{"/data/rootfs.erofs", "erofs"},
		{"/data/rootfs.sqfs", "squashfs"},
	}
	var lastErr error
	for _, c := range candidates {
		if _, err := os.Stat(c.img); err != nil {
			continue
		}
		if out, err := losetup("-r", "/dev/loop1", c.img); err != nil {
			lastErr = fmt.Errorf("losetup %s: %v %s", c.img, err, out)
			continue
		}
		if err := mount("/dev/loop1", "/ro", c.fstype, unix.MS_RDONLY, ""); err != nil {
			lastErr = fmt.Errorf("mount %s: %v", c.fstype, err)
			_ = losetupDetach("/dev/loop1")
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum rootfs encontrado")
	}
	return lastErr
}

func losetupDetach(dev string) error {
	return exec.Command("/bin/busybox", "losetup", "-d", dev).Run()
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
