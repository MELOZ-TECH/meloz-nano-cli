package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const (
	stateDir   = ".nanoc"
	ctlName    = "nanoc.sock"
	dockerSock = "docker.sock"
)

func paths() (dir, ctl, docker string) {
	home, _ := os.UserHomeDir()
	dir = filepath.Join(home, stateDir)
	_ = os.MkdirAll(dir, 0o755)
	return dir, filepath.Join(dir, ctlName), filepath.Join(dir, dockerSock)
}

func ctlRequestRaw(payload map[string]string, timeout time.Duration) (map[string]any, error) {
	_, ctl, _ := paths()
	c, err := net.DialTimeout("unix", ctl, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(c).Encode(payload); err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func ctlRequest(cmd string, timeout time.Duration) (map[string]any, error) {
	_, ctl, _ := paths()
	c, err := net.DialTimeout("unix", ctl, timeout)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(map[string]string{"cmd": cmd}); err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func cli(cmd string) error {
	_, ctl, docker := paths()
	switch cmd {
	case "status":
		resp, err := ctlRequest("status", time.Second)
		if err != nil {
			fmt.Println("nanoc: parado")
			return nil
		}
		fmt.Printf("nanoc: rodando (pid %v) docker=%v\n", resp["pid"], resp["docker_sock"])
		return nil
	case "down":
		if _, err := ctlRequest("stop", 2*time.Second); err != nil {
			if b, rerr := os.ReadFile(ctl + ".pid"); rerr == nil {
				var pid int
				fmt.Sscanf(string(b), "%d", &pid)
				if pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGTERM)
				}
			}
			fmt.Println("nanoc: já está parado")
			return nil
		}
		// espera o daemon sumir de fato (o VM pode levar alguns segundos)
		for i := 0; i < 150; i++ {
			if _, err := ctlRequest("status", 200*time.Millisecond); err != nil {
				fmt.Println("nanoc: parado")
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Println("nanoc: parando (timeout aguardando saída)")
		return nil
	case "up":
		if _, err := ctlRequest("status", 500*time.Millisecond); err == nil {
			fmt.Println("nanoc: já rodando")
			return nil
		}
		// limpa daemon zumbi que ainda segura data.img
		if b, rerr := os.ReadFile(ctl + ".pid"); rerr == nil {
			var pid int
			fmt.Sscanf(string(b), "%d", &pid)
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				for i := 0; i < 50; i++ {
					if err := syscall.Kill(pid, 0); err != nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
		_ = os.Remove(ctl + ".pid")
		time.Sleep(2 * time.Second)
		self, err := os.Executable()
		if err != nil {
			return err
		}
		stateDir, _, _ := paths()
		assets := envOr("NANOC_ASSETS", filepath.Join(stateDir, "assets"))
		rootfs := envOr("NANOC_ROOTFS", stateDir)
		logf, err := os.OpenFile(filepath.Join(stateDir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		c := exec.Command(self, "daemon",
			filepath.Join(assets, "Image"), filepath.Join(assets, "initramfs.cpio.gz"),
			rootfs, docker, ctl)
		c.Stdout = logf
		c.Stderr = logf
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			return err
		}
		for i := 0; i < 120; i++ {
			if _, err := ctlRequest("status", 300*time.Millisecond); err == nil {
				fmt.Printf("nanoc: pronto → DOCKER_HOST=unix://%s\n", docker)
				return nil
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("daemon não respondeu; veja %s/daemon.log", stateDir)
	}
	return fmt.Errorf("comando desconhecido: %s", cmd)
}

// k8sCmd liga/desliga/consulta o Kubernetes opcional no guest.
func k8sCmd(action string) error {
	switch action {
	case "enable", "disable", "status", "reset":
	default:
		return fmt.Errorf("uso: nanoc k8s [enable|disable|status]")
	}
	resp, err := ctlRequestRaw(map[string]string{"cmd": "k8s", "action": action}, 30*time.Minute)
	if err != nil {
		return err
	}
	if msg, ok := resp["message"].(string); ok && msg != "" {
		fmt.Println(msg)
	}
	on, _ := resp["k8s"].(bool)
	switch action {
	case "enable":
		if !on {
			return fmt.Errorf("kubernetes não subiu")
		}
		dir, _, _ := paths()
		src := filepath.Join(dir, "k3s.yaml")
		dst := filepath.Join(dir, "kubeconfig")
		if b, err := os.ReadFile(src); err == nil {
			if err := os.WriteFile(dst, b, 0o600); err == nil {
				fmt.Printf("kubeconfig: %s\nexport KUBECONFIG=%s\n", dst, dst)
			}
		}
	case "status":
		if on {
			fmt.Println("kubernetes: ligado")
		} else {
			fmt.Println("kubernetes: desligado")
		}
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
