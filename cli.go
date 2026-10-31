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
	stateDir   = ".nano"
	ctlName    = "nano.sock"
	dockerSock = "docker.sock"
)

func paths() (dir, ctl, docker string) {
	home, _ := os.UserHomeDir()
	dir = filepath.Join(home, stateDir)
	_ = os.MkdirAll(dir, 0o755)
	return dir, filepath.Join(dir, ctlName), filepath.Join(dir, dockerSock)
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
	dir, ctl, docker := paths()
	switch cmd {
	case "status":
		resp, err := ctlRequest("status", time.Second)
		if err != nil {
			fmt.Println("nano: parado")
			return nil
		}
		fmt.Printf("nano: rodando (pid %v) docker=%v\n", resp["pid"], resp["docker_sock"])
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
			fmt.Println("nano: já está parado")
			return nil
		}
		// espera o daemon sumir de fato (o VM pode levar alguns segundos)
		for i := 0; i < 150; i++ {
			if _, err := ctlRequest("status", 200*time.Millisecond); err != nil {
				fmt.Println("nano: parado")
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Println("nano: parando (timeout aguardando saída)")
		return nil
	case "up":
		if _, err := ctlRequest("status", 500*time.Millisecond); err == nil {
			fmt.Println("nano: já rodando")
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
		assets := envOr("NANO_ASSETS", "assets")
		rootfs := envOr("NANO_ROOTFS", "rootfs")
		logf, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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
				fmt.Printf("nano: pronto → DOCKER_HOST=unix://%s\n", docker)
				return nil
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("daemon não respondeu; veja %s/daemon.log", dir)
	}
	return fmt.Errorf("comando desconhecido: %s", cmd)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
