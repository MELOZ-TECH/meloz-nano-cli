//go:build linux

// nanoc-agent: roda no guest. Expõe o docker.sock em :2375 e um pequeno canal de
// controle em :2376 (info e ciclo de vida opcional do Kubernetes/k3s).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	dockerPort  = 2375
	controlPort = 2376
	dockerSock  = "/var/run/docker.sock"

	k3sBin        = "/data/k3s"
	k3sData       = "/var/lib/docker/k3s"
	k3sKubeconfig = "/data/k3s.yaml"
	k3sPID        = "/data/k3s.pid"
	k3sURL        = "https://github.com/k3s-io/k3s/releases/latest/download/k3s-arm64"
)

type request struct {
	Cmd    string `json:"cmd"`
	Action string `json:"action,omitempty"`
}

type response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	K8s     bool   `json:"k8s"`
	Version string `json:"version,omitempty"`
}

func main() {
	go serveControl()
	serveDocker()
}

func serveDocker() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", dockerPort))
	if err != nil {
		log.Fatalf("listen docker: %v", err)
	}
	log.Printf("docker proxy em :%d -> %s", dockerPort, dockerSock)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go proxy(conn)
	}
}

func proxy(conn net.Conn) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic no proxy: %v", r)
		}
	}()
	up, err := net.Dial("unix", dockerSock)
	if err != nil {
		return
	}
	defer up.Close()
	go io.Copy(up, conn)
	io.Copy(conn, up)
}

func serveControl() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", controlPort))
	if err != nil {
		log.Fatalf("listen control: %v", err)
	}
	log.Printf("controle em :%d", controlPort)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleControl(conn)
	}
}

func handleControl(conn net.Conn) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic no controle: %v", r)
		}
	}()
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	resp := dispatch(req)
	_ = json.NewEncoder(conn).Encode(resp)
}

func dispatch(req request) response {
	switch req.Cmd {
	case "info":
		return response{OK: true, K8s: k3sRunning(), Version: strings.TrimSpace(run("k3s --version"))}
	case "k8s-enable":
		if err := k3sEnable(); err != nil {
			return response{OK: false, Message: err.Error(), K8s: k3sRunning()}
		}
		return response{OK: true, K8s: true, Message: "kubernetes habilitado"}
	case "k8s-disable":
		k3sDisable()
		return response{OK: true, K8s: false, Message: "kubernetes desabilitado"}
	case "k8s-reset":
		k3sDisable()
		_ = os.RemoveAll(k3sData)
		_ = os.Remove(k3sKubeconfig)
		return response{OK: true, K8s: false, Message: "estado do kubernetes removido"}
	case "k8s-status":
		return response{OK: true, K8s: k3sRunning()}
	}
	return response{OK: false, Message: "comando desconhecido"}
}

func run(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return string(out)
}

func k3sRunning() bool {
	b, err := os.ReadFile(k3sPID)
	if err != nil {
		return false
	}
	var pid int
	fmt.Sscanf(string(b), "%d", &pid)
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// evita falso positivo por reuso de PID
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return err == nil && strings.Contains(string(cmdline), "k3s")
}

func k3sEnable() error {
	if _, err := os.Stat(k3sBin); err != nil {
		log.Printf("baixando k3s")
		if out, err := exec.Command("wget", "-q", "-O", k3sBin+".tmp", k3sURL).CombinedOutput(); err != nil {
			return fmt.Errorf("download do k3s falhou: %v %s", err, strings.TrimSpace(string(out)))
		}
		if err := os.Rename(k3sBin+".tmp", k3sBin); err != nil {
			return err
		}
	}
	if err := os.Chmod(k3sBin, 0o755); err != nil {
		return err
	}
	if k3sRunning() {
		return nil
	}
	// kubeconfig antigo não serve como sinal de prontidão
	_ = os.Remove(k3sKubeconfig)
	_ = os.MkdirAll(k3sData, 0o755)
	cmd := exec.Command(k3sBin, "server",
		"--data-dir", k3sData,
		"--write-kubeconfig", k3sKubeconfig,
		"--write-kubeconfig-mode", "644",
		"--disable", "traefik",
		"--disable", "servicelb",
	)
	logFile, err := os.OpenFile("/data/k3s.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = os.WriteFile(k3sPID, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for i := 0; i < 240; i++ {
		select {
		case err := <-exited:
			return fmt.Errorf("k3s terminou cedo (%v); veja /data/k3s.log", err)
		default:
		}
		if _, err := os.Stat(k3sKubeconfig); err == nil {
			if c, err := net.DialTimeout("tcp", "127.0.0.1:6443", time.Second); err == nil {
				c.Close()
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("k3s não respondeu em :6443; veja /data/k3s.log")
}

func k3sDisable() {
	b, err := os.ReadFile(k3sPID)
	if err == nil {
		var pid int
		fmt.Sscanf(string(b), "%d", &pid)
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			for i := 0; i < 30; i++ {
				if syscall.Kill(pid, 0) != nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = os.Remove(k3sPID)
}
