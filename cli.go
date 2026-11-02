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

func ctlRequestRaw(payload map[string]any, timeout time.Duration) (map[string]any, error) {
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
	case "ports":
		resp, err := ctlRequest("status", time.Second)
		if err != nil {
			fmt.Println("nanoc: parado")
			return nil
		}
		ports, _ := resp["ports"].([]any)
		if len(ports) == 0 {
			fmt.Println("nenhuma porta publicada")
			return nil
		}
		addr := "127.0.0.1"
		if b, ok := resp["bind"].(string); ok && b != "" {
			addr = b
		}
		for _, p := range ports {
			fmt.Printf("%s:%v\n", addr, p)
		}
		return nil
	case "status":
		resp, err := ctlRequest("status", time.Second)
		if err != nil {
			fmt.Println("nanoc: parado")
			return nil
		}
		fmt.Printf("nanoc: rodando (pid %v) docker=%v\n", resp["pid"], resp["docker_sock"])
		if t, ok := resp["mem_target_mb"]; ok {
			fmt.Printf("memória: %v MB de até %v MB (ajuste automático)\n", t, resp["mem_max_mb"])
		} else if m, ok := resp["mem_max_mb"]; ok {
			fmt.Printf("memória: %v MB fixa\n", m)
		}
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
		stateDir, _, _ := paths()
		// O lock do daemon só existe enquanto o processo vive. Preso, o daemon está
		// ligando a VM ou lento demais para responder (carga alta no Mac): matar
		// derrubava containers rodando. Espera ele em vez disso.
		if daemonAlive(ctl) {
			fmt.Println("nanoc: daemon já existe e ainda não respondeu; aguardando")
			return waitReady(stateDir, docker)
		}
		// ninguém segura o lock: o .pid é de um daemon morto, e o pid pode já ser de
		// outro processo
		_ = os.Remove(ctl + ".pid")
		self, err := os.Executable()
		if err != nil {
			return err
		}
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
		return waitReady(stateDir, docker)
	}
	return fmt.Errorf("comando desconhecido: %s", cmd)
}

// waitReady espera o daemon responder ao status por até dois minutos.
func waitReady(stateDir, docker string) error {
	for i := 0; i < 120; i++ {
		if _, err := ctlRequest("status", time.Second); err == nil {
			fmt.Printf("nanoc: pronto → DOCKER_HOST=unix://%s\n", docker)
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("daemon não respondeu; veja %s/daemon.log", stateDir)
}

// execCmd roda um comando dentro de um container, via agente no guest.
func execCmd(container string, argv []string) error {
	resp, err := ctlRequestRaw(map[string]any{
		"cmd":       "exec",
		"container": container,
		"argv":      argv,
	}, 10*time.Minute)
	if err != nil {
		return err
	}
	if out, ok := resp["output"].(string); ok && out != "" {
		fmt.Print(out)
	}
	if msg, ok := resp["message"].(string); ok && msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	code := 0
	if c, ok := resp["exit_code"].(float64); ok {
		code = int(c)
	}
	if ok, _ := resp["ok"].(bool); !ok || code != 0 {
		if code != 0 {
			os.Exit(code)
		}
		return fmt.Errorf("exec falhou")
	}
	return nil
}

// dockerPassthrough executa o docker CLI do host apontando para o socket do nanoc.
func dockerPassthrough(cmd string, args []string) error {
	path, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker CLI não encontrado no PATH")
	}
	clean := make([]string, 0, len(args))
	for _, a := range args {
		if a != "--" {
			clean = append(clean, a)
		}
	}
	args = clean
	_, _, docker := paths()
	c := exec.Command(path, append([]string{cmd}, args...)...)
	c.Env = append(os.Environ(), "DOCKER_HOST=unix://"+docker)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// k8sCmd liga/desliga/consulta o Kubernetes opcional no guest.
func k8sCmd(action string) error {
	switch action {
	case "enable", "disable", "status", "reset":
	default:
		return fmt.Errorf("uso: nanoc k8s [enable|disable|status]")
	}
	resp, err := ctlRequestRaw(map[string]any{"cmd": "k8s", "action": action}, 30*time.Minute)
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
	case "ports":
		resp, err := ctlRequest("status", time.Second)
		if err != nil {
			fmt.Println("nanoc: parado")
			return nil
		}
		ports, _ := resp["ports"].([]any)
		if len(ports) == 0 {
			fmt.Println("nenhuma porta publicada")
			return nil
		}
		addr := "127.0.0.1"
		if b, ok := resp["bind"].(string); ok && b != "" {
			addr = b
		}
		for _, p := range ports {
			fmt.Printf("%s:%v\n", addr, p)
		}
		return nil
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
