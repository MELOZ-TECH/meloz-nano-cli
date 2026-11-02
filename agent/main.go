//go:build linux

// nanoc-agent: roda no guest. Expõe o docker.sock na porta vsock 2375 e um pequeno
// canal de controle na 2376 (info, run/exec e ciclo de vida opcional do k3s).
// Só por vsock: em TCP, qualquer processo do Mac ou VM da rede NAT teria root no
// guest. TCP só com nanoc.tcp=1 no cmdline (NANOC_VSOCK=0 no host, para depuração).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"log"
	"net"
	"net/http"
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
	Cmd       string   `json:"cmd"`
	Action    string   `json:"action,omitempty"`
	Container string   `json:"container,omitempty"`
	Argv      []string `json:"argv,omitempty"`
}

type response struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	K8s      bool   `json:"k8s"`
	Version  string `json:"version,omitempty"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

func main() {
	if tcpEnabled() {
		go serveTCP(controlPort, func(c net.Conn) { handleControl(c) })
		go serveTCP(dockerPort, proxy)
	}
	go serveVsock(controlPort, handleControl)
	serveVsock(dockerPort, proxyDocker)
}

// tcpEnabled: TCP só quando o host pediu (nanoc.tcp=1 no cmdline do kernel).
func tcpEnabled() bool {
	b, _ := os.ReadFile("/proc/cmdline")
	for _, f := range strings.Fields(string(b)) {
		if f == "nanoc.tcp=1" {
			return true
		}
	}
	return false
}

// serveVsock atende uma porta AF_VSOCK e entrega cada conexão a handle.
func serveVsock(port uint32, handle func(io.ReadWriteCloser)) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("vsock indisponível: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		log.Fatalf("vsock bind :%d: %v", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		log.Fatalf("vsock listen :%d: %v", port, err)
	}
	log.Printf("vsock :%d ativo", port)
	for {
		nfd, _, err := unix.Accept(fd)
		if err != nil {
			log.Printf("vsock accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// não-bloqueante: o os.File entra no poller e Close interrompe leituras pendentes
		if err := unix.SetNonblock(nfd, true); err != nil {
			unix.Close(nfd)
			continue
		}
		go handle(&vsockConn{File: os.NewFile(uintptr(nfd), "vsock")})
	}
}

func proxyDocker(c io.ReadWriteCloser) {
	up, err := net.Dial("unix", dockerSock)
	if err != nil {
		log.Printf("dial docker: %v", err)
		c.Close()
		return
	}
	relay(c, up)
}

// vsockConn adiciona half-close a um socket AF_VSOCK aberto como *os.File.
// Usa SyscallConn em vez de Fd(), que voltaria o descritor para modo bloqueante.
type vsockConn struct{ *os.File }

func (v *vsockConn) CloseWrite() error {
	rc, err := v.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = unix.Shutdown(int(fd), unix.SHUT_WR) }); err != nil {
		return err
	}
	return serr
}

func serveTCP(port int, handle func(net.Conn)) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("listen tcp :%d: %v", port, err)
	}
	log.Printf("tcp :%d ativo (nanoc.tcp=1)", port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(conn)
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
	relay(conn, up)
}

func handleControl(conn io.ReadWriteCloser) {
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
	case "run":
		out, code, err := guestRun(req.Argv)
		if err != nil {
			return response{OK: false, Message: err.Error(), Output: out, ExitCode: code}
		}
		return response{OK: true, Output: out, ExitCode: code}
	case "exec":
		out, code, err := containerExec(req.Container, req.Argv)
		if err != nil {
			return response{OK: false, Message: err.Error(), Output: out, ExitCode: code}
		}
		return response{OK: true, Output: out, ExitCode: code}
	case "k8s-status":
		return response{OK: true, K8s: k3sRunning()}
	}
	return response{OK: false, Message: "comando desconhecido"}
}

func run(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return string(out)
}

// containerExec entra nos namespaces do container e executa o comando,
// sem passar pelo runc exec (que não resolve o rootfs neste kernel).
// guestRun executa um comando direto no guest (usado pelas máquinas).
func guestRun(argv []string) (string, int, error) {
	if len(argv) == 0 {
		return "", 1, fmt.Errorf("argv vazio")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
		err = nil
	}
	return string(out), code, err
}

func containerExec(id string, argv []string) (string, int, error) {
	if id == "" || len(argv) == 0 {
		return "", 1, fmt.Errorf("uso: exec <container> <cmd...>")
	}
	pid, err := containerPID(id)
	if err != nil {
		return "", 1, err
	}
	args := []string{"-t", fmt.Sprintf("%d", pid), "-m", "-u", "-i", "-n", "-p", "-r"}
	args = append(args, argv...)
	cmd := exec.Command("nsenter", args...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
		err = nil
	}
	return string(out), code, err
}

func containerPID(id string) (int, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", dockerSock)
		},
	}
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := c.Get("http://docker/containers/" + id + "/json")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var info struct {
		State struct {
			Pid int `json:"Pid"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, err
	}
	if info.State.Pid <= 0 {
		return 0, fmt.Errorf("container %s não está rodando", id)
	}
	return info.State.Pid, nil
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
