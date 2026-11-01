package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const releaseBase = "https://github.com/MELOZ-TECH/nano-container/releases/download"

// bootstrapAssets baixa kernel/initramfs/modloop/rootfs do release para ~/.nanoc/assets.
func bootstrapAssets() error {
	dir, _, _ := paths()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		return err
	}
	want := []string{"Image", "initramfs.cpio.gz", "modloop-virt", "rootfs.sqfs"}

	missing := false
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(assets, name)); err != nil {
			missing = true
			break
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "rootfs.sqfs")); err != nil {
		missing = true
	}
	if !missing {
		fmt.Println("assets já presentes em", assets)
		return nil
	}

	v := strings.TrimPrefix(version, "v")
	if v == "" || v == "dev" {
		return fmt.Errorf("versão de desenvolvimento: rode ./scripts/bootstrap.sh no repositório")
	}
	url := fmt.Sprintf("%s/v%s/nanoc-assets-v%s.tar.gz", releaseBase, v, v)
	fmt.Println("baixando", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("assets não publicados para v%s (HTTP %d); use ./scripts/bootstrap.sh", v, resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || name == "." {
			continue
		}
		dest := filepath.Join(assets, name)
		if name == "rootfs.sqfs" || name == "data.img" {
			dest = filepath.Join(dir, name)
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
		n++
	}
	fmt.Printf("assets instalados em %s (%d arquivos)\n", assets, n)
	return nil
}

func printUsage() {
	fmt.Println(`nanoc — container runtime CLI para macOS (Apple Silicon)

uso:
  nanoc up                 sobe a VM e o daemon
  nanoc down               para a VM
  nanoc status             mostra o estado
  nanoc bootstrap          baixa kernel/rootfs do release
  nanoc k8s <ação>         enable | disable | status | reset
  nanoc --version          versão

  export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock`)
}
