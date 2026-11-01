package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// doctorCmd verifica os pré-requisitos e o estado do runtime.
func doctorCmd() error {
	dir, ctl, docker := paths()
	assets := filepath.Join(dir, "assets")
	fail := 0

	check := func(name string, ok bool, detail string) {
		status := "ok  "
		if !ok {
			status = "FALHA"
			fail++
		}
		fmt.Printf("[%s] %-22s %s\n", status, name, detail)
	}

	self, _ := os.Executable()
	out, _ := exec.Command("codesign", "-d", "--entitlements", "-", self).CombinedOutput()
	check("entitlement", strings.Contains(string(out), "com.apple.security.virtualization"),
		"assinatura com com.apple.security.virtualization")

	for _, f := range []string{"Image", "initramfs.cpio.gz", "modloop-virt"} {
		p := filepath.Join(assets, f)
		st, err := os.Stat(p)
		check("asset "+f, err == nil && st.Size() > 0, p)
	}

	rootfs := ""
	for _, name := range []string{"rootfs.erofs", "rootfs.sqfs"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			rootfs = p
			break
		}
	}
	check("rootfs", rootfs != "", rootfs)

	data := filepath.Join(dir, "data.img")
	if st, err := os.Stat(data); err == nil {
		check("data.img", true, fmt.Sprintf("%s (%.1f GB)", data, float64(st.Size())/(1<<30)))
	} else {
		check("data.img", true, "será criado no primeiro boot")
	}

	if _, err := ctlRequest("status", 500*1e6); err == nil {
		check("daemon", true, ctl)
		if _, err := os.Stat(docker); err == nil {
			check("docker.sock", true, docker)
		} else {
			check("docker.sock", false, docker)
		}
	} else {
		check("daemon", false, "parado (rode: nanoc up)")
	}

	if fail > 0 {
		return fmt.Errorf("%d verificação(ões) falharam", fail)
	}
	fmt.Println("\ntudo pronto.")
	return nil
}
