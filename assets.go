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
	"time"
)

const releaseBase = "https://github.com/MELOZ-TECH/nano-container/releases/download"

// bootstrapAssets baixa kernel/initramfs/modloop/rootfs do release para ~/.nanoc/assets.
func bootstrapAssets() error {
	dir, _, _ := paths()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		return err
	}
	want := []string{"Image", "initramfs.cpio.gz", "modloop-virt"}

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
	v := strings.TrimPrefix(version, "v")
	dev := v == "" || v == "dev"
	// as correções do guest vivem nos assets: versão diferente do binário = baixar de novo
	stampPath := filepath.Join(assets, ".version")
	stamp, _ := os.ReadFile(stampPath)
	current := strings.TrimSpace(string(stamp)) == v
	if !missing && (dev || current) {
		fmt.Println("assets já presentes em", assets)
		return nil
	}
	if dev {
		return fmt.Errorf("versão de desenvolvimento: rode ./scripts/bootstrap.sh no repositório")
	}
	if !missing {
		// a VM lê o rootfs direto destes arquivos: sobrescrever com ela ligada corrompe o guest
		if _, err := ctlRequest("status", 500*time.Millisecond); err == nil {
			return fmt.Errorf("assets de outra versão; rode `nanoc down` e depois `nanoc bootstrap`")
		}
		fmt.Printf("assets de outra versão (%q); atualizando para v%s\n", strings.TrimSpace(string(stamp)), v)
	}
	// o nome do arquivo variou entre releases; tenta as duas formas
	var resp *http.Response
	var err error
	for _, name := range []string{"nanoc-assets-" + v, "nanoc-assets-v" + v} {
		url := fmt.Sprintf("%s/v%s/%s.tar.gz", releaseBase, v, name)
		fmt.Println("baixando", url)
		resp, err = http.Get(url)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusOK {
			break
		}
		resp.Body.Close()
		resp = nil
	}
	if resp == nil {
		return fmt.Errorf("assets não publicados para v%s; use ./scripts/bootstrap.sh", v)
	}
	defer resp.Body.Close()
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
		if name == "rootfs.sqfs" || name == "rootfs.erofs" || name == "data.img" {
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
	if err := os.WriteFile(stampPath, []byte(v+"\n"), 0o644); err != nil {
		return err
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
  nanoc ports              lista portas publicadas em 127.0.0.1
  nanoc exec <c> -- cmd    docker exec no guest
  nanoc logs <c>           docker logs no guest
  nanoc bootstrap          baixa kernel/rootfs do release
  nanoc k8s <ação>         enable | disable | status | reset
  nanoc --version          versão

  export DOCKER_HOST=unix://$HOME/.nanoc/docker.sock`)
}
